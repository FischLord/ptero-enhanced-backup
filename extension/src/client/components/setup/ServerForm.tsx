import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, Button, Input, Label, NumberInput, Select, Spinner, toast } from '@pterodactyl/sdk';
import { EXTENSION_ID, createGatewayServer, errorInfo, getSetupAllocations, getSetupNodes, keys, searchSetupUsers } from '../../api';
import { useT } from '../../i18n';
import type { CreatedGatewayServer, SetupState } from '../../types';
import { setupErrorText } from './setupErrors';

function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: React.ReactNode }) {
    return (
        <div className={'peb:flex peb:flex-col peb:gap-1'}>
            <Label htmlFor={id}>{label}</Label>
            {children}
            {hint && <p className={'peb:text-xs peb:text-muted-foreground'}>{hint}</p>}
        </div>
    );
}

export default function ServerForm({ setup, onCreated }: { setup: SetupState; onCreated: (created: CreatedGatewayServer) => void }) {
    const { t } = useT();
    const queryClient = useQueryClient();
    const d = setup.defaults;
    const [name, setName] = useState(d.name);
    const [owner, setOwner] = useState<number | null>(null);
    const [ownerSearch, setOwnerSearch] = useState('');
    const [node, setNode] = useState<number | null>(null);
    const [allocation, setAllocation] = useState<number | null>(null);
    const [memory, setMemory] = useState<number | null>(d.memory);
    const [disk, setDisk] = useState<number | null>(d.disk);
    const [bucket, setBucket] = useState(d.bucket);
    const [region, setRegion] = useState(d.region);
    const [repository, setRepository] = useState(d.repository);
    const [host, setHost] = useState('');
    const [maybeExists, setMaybeExists] = useState(false);

    const nodes = useQuery({ queryKey: keys.setupNodes(), queryFn: getSetupNodes, retry: false, enabled: setup.egg.exists });
    const allocations = useQuery({ queryKey: keys.setupAllocations(node ?? 0), queryFn: () => getSetupAllocations(node as number), retry: false, enabled: node !== null });
    const users = useQuery({ queryKey: keys.setupUsers(ownerSearch), queryFn: () => searchSetupUsers(ownerSearch), retry: false, enabled: setup.egg.exists });

    const create = useMutation({
        mutationFn: () =>
            createGatewayServer({
                name: name.trim(),
                owner_id: owner as number,
                allocation_id: allocation as number,
                memory: memory as number,
                disk: disk as number,
                bucket: bucket.trim(),
                region: region.trim(),
                repository: repository.trim(),
                host: host.trim(),
            }),
        onMutate: () => setMaybeExists(false),
        onSuccess: (created) => {
            onCreated(created);
            create.reset();
            void queryClient.invalidateQueries({ queryKey: keys.setupNodes() });
        },
        onError: (error) => {
            toast.error(setupErrorText(error, t, 'setup_server_failed'));
            // Antwort verloren oder Wiederholung auf eine belegte Allocation: Der Server kann schon existieren, die Werte sind dann nur noch im Panel.
            const info = errorInfo(error);
            setMaybeExists(info.code === 'allocation_unavailable' || ((info.status === null || info.status >= 500) && info.code !== 'wings_unreachable'));
            void queryClient.invalidateQueries({ queryKey: [EXTENSION_ID, 'setup'] });
        },
    });

    if (!setup.available.server_create) {
        return (
            <Alert type={'warning'} title={t('setup_manual_server_title')}>
                {t('setup_manual_server')}
            </Alert>
        );
    }
    if (!setup.egg.exists) return <Alert type={'info'}>{t('setup_server_need_egg')}</Alert>;

    const selectedAllocation = allocations.data?.items.find((a) => a.id === allocation);
    const hostOk = selectedAllocation?.alias != null || host.trim() !== '';
    const complete = hostOk && name.trim() !== '' && owner !== null && allocation !== null && !!memory && !!disk && bucket.trim() !== '' && region.trim() !== '';

    return (
        <form
            className={'peb:flex peb:flex-col peb:gap-4'}
            onSubmit={(event) => {
                event.preventDefault();
                if (complete && !create.isPending) create.mutate();
            }}
        >
            <p className={'peb:text-sm peb:text-muted-foreground'}>{t('setup_server_intro')}</p>
            <div className={'peb:grid peb:grid-cols-1 peb:gap-4 peb:md:grid-cols-2'}>
                <Field id={'peb-setup-name'} label={t('setup_field_name')}>
                    <Input id={'peb-setup-name'} value={name} onChange={(e) => setName(e.target.value)} maxLength={191} />
                </Field>
                <Field id={'peb-setup-owner'} label={t('setup_field_owner')} hint={t('setup_owner_note')}>
                    <Select
                        id={'peb-setup-owner'}
                        value={owner}
                        onChange={(v) => setOwner(Number(v))}
                        onSearchChange={setOwnerSearch}
                        searchPlaceholder={t('setup_field_owner_search')}
                        placeholder={t('setup_pick')}
                        options={(users.data?.items ?? []).map((u) => ({ value: u.id, label: `${u.username} (${u.email})`, textLabel: `${u.username} ${u.email}` }))}
                    />
                </Field>
                <Field id={'peb-setup-node'} label={t('setup_field_node')}>
                    {nodes.isLoading ? (
                        <Spinner size={'small'} />
                    ) : (
                        <Select
                            id={'peb-setup-node'}
                            value={node}
                            onChange={(v) => {
                                setNode(Number(v));
                                setAllocation(null);
                                setHost(nodes.data?.items.find((n) => n.id === Number(v))?.fqdn ?? '');
                            }}
                            placeholder={nodes.data && nodes.data.items.length === 0 ? t('setup_no_nodes') : t('setup_pick')}
                            options={(nodes.data?.items ?? []).map((n) => ({ value: n.id, label: t('setup_node_option', { name: n.name, free: n.free_allocations }), textLabel: n.name, disabled: n.free_allocations === 0 }))}
                        />
                    )}
                </Field>
                <Field id={'peb-setup-allocation'} label={t('setup_field_allocation')}>
                    <Select
                        id={'peb-setup-allocation'}
                        value={allocation}
                        onChange={(v) => setAllocation(Number(v))}
                        disabled={node === null}
                        placeholder={allocations.data && allocations.data.items.length === 0 ? t('setup_no_allocations') : t('setup_pick')}
                        options={(allocations.data?.items ?? []).map((a) => ({ value: a.id, label: `${a.alias ?? a.ip}:${a.port}` }))}
                    />
                </Field>
                <Field id={'peb-setup-host'} label={t('setup_field_host')} hint={t('setup_field_host_hint')}>
                    <Input id={'peb-setup-host'} value={host} onChange={(e) => setHost(e.target.value)} maxLength={255} disabled={selectedAllocation?.alias != null} />
                </Field>
                <Field id={'peb-setup-memory'} label={t('setup_field_memory')}>
                    <NumberInput id={'peb-setup-memory'} value={memory} onValueChange={setMemory} min={256} step={256} />
                </Field>
                <Field id={'peb-setup-disk'} label={t('setup_field_disk')}>
                    <NumberInput id={'peb-setup-disk'} value={disk} onValueChange={setDisk} min={1024} step={1024} />
                </Field>
                <Field id={'peb-setup-bucket'} label={t('setup_field_bucket')}>
                    <Input id={'peb-setup-bucket'} value={bucket} onChange={(e) => setBucket(e.target.value)} maxLength={63} />
                </Field>
                <Field id={'peb-setup-region'} label={t('setup_field_region')}>
                    <Input id={'peb-setup-region'} value={region} onChange={(e) => setRegion(e.target.value)} maxLength={32} />
                </Field>
            </div>
            <Field id={'peb-setup-repository'} label={t('setup_field_repository')} hint={t('setup_field_repository_hint')}>
                <Input id={'peb-setup-repository'} value={repository} onChange={(e) => setRepository(e.target.value)} maxLength={255} placeholder={'/mnt/peb/repo'} />
            </Field>
            {maybeExists && <Alert type={'warning'}>{t('setup_server_maybe_exists')}</Alert>}
            <div>
                <Button type={'submit'} color={'primary'} disabled={!complete || create.isPending}>
                    {t('setup_create_server')}
                </Button>
            </div>
        </form>
    );
}
