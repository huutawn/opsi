import { useState } from "react";
import { Badge, Button, Icon, Input } from "@/components/ui/primitives";
import { PlanCheck } from "@/features/deploy/plan-form-controls";
import { SecretConfigurationEditor } from "@/features/deploy/secret-configuration-editor";
import { getApplicationEffectiveKeys, isApplicationConfirmed, isSecretLikeEnvironmentName, isValidEnvironmentName } from "@/features/deploy/runtime-config";
import type { DeploymentPlan, ServiceRecord, WorkloadSecretMetadata } from "@/lib/contracts/registry";

const noStoredSecrets = async () => [] as WorkloadSecretMetadata[];

type Props = {
  application: DeploymentPlan["applications"][number];
  applicationIndex: number;
  canEdit: boolean;
  listSecrets?: (applicationID: string) => Promise<WorkloadSecretMetadata[]>;
  plan: DeploymentPlan;
  resolveSecret: (applicationID: string, logicalName: string, value: string) => Promise<WorkloadSecretMetadata>;
  services: ServiceRecord[];
  update: (change: (draft: DeploymentPlan) => void) => void;
};

export function RuntimeConfigurationEditor({ application, applicationIndex, canEdit, listSecrets, plan, resolveSecret, services, update }: Props) {
  const [newName, setNewName] = useState("");
  const [newValue, setNewValue] = useState("");
  const [renaming, setRenaming] = useState("");
  const [renamedName, setRenamedName] = useState("");
  const [error, setError] = useState("");
  const keys = getApplicationEffectiveKeys(plan, application);
  const confirmed = isApplicationConfirmed(plan, application);
  const applicationID = services.find((service) => service.name === application.key)?.id || application.key;

  function clearConfirmation(draft: DeploymentPlan) {
    draft.application_environment_reviews = (draft.application_environment_reviews || []).filter((review) => review.application_source_key !== application.source_key);
  }

  function clearConfirmationIfEmpty(draft: DeploymentPlan) {
    if (getApplicationEffectiveKeys(draft, draft.applications[applicationIndex]).total === 0) clearConfirmation(draft);
  }

  function validatePlainName(name: string, previous = ""): string {
    if (!isValidEnvironmentName(name)) return "Environment key must start with a letter or underscore and contain only letters, numbers, and underscores.";
    if (isSecretLikeEnvironmentName(name)) return "Secret-like keys such as PASSWORD, API_KEY, and TOKEN must use Add secret.";
    const reserved = [...Object.keys(application.environment || {}).filter((item) => item !== previous), ...keys.secrets.map((secret) => secret.environment_name), ...keys.generated.map((key) => key.name)];
    return reserved.includes(name) ? `Environment key “${name}” already exists in this application.` : "";
  }

  function addPlain() {
    const name = newName.trim();
    const validation = validatePlainName(name);
    if (validation) return setError(validation);
    if (Object.keys(application.environment || {}).length >= 64) return setError("An application can define at most 64 plain environment variables.");
    if (newValue.length > 4096 || /[\x00\r\n]/.test(newValue)) return setError("Environment values must be at most 4096 characters and cannot contain NUL or line breaks.");
    update((draft) => {
      draft.applications[applicationIndex].environment ??= {};
      draft.applications[applicationIndex].environment![name] = newValue;
      clearConfirmation(draft);
    });
    setNewName("");
    setNewValue("");
    setError("");
  }

  function renamePlain(previous: string) {
    const name = renamedName.trim();
    if (name === previous) return setRenaming("");
    const validation = validatePlainName(name, previous);
    if (validation) return setError(validation);
    update((draft) => {
      const environment = draft.applications[applicationIndex].environment || {};
      const value = environment[previous];
      delete environment[previous];
      environment[name] = value;
      draft.applications[applicationIndex].environment = environment;
    });
    setRenaming("");
    setRenamedName("");
    setError("");
  }

  const applicationSecrets = keys.secrets;
  const reservedForSecrets = [...Object.keys(application.environment || {}), ...applicationSecrets.map((secret) => secret.environment_name), ...keys.generated.map((key) => key.name)];

  return (
    <section aria-labelledby={`runtime-heading-${applicationIndex}`} className="mt-6 border-t border-outline-variant/30 pt-4" id={`application-runtime-${applicationIndex}`} tabIndex={-1}>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 className="flex items-center gap-2 text-base font-semibold" id={`runtime-heading-${applicationIndex}`}><Icon name="settings" />Runtime configuration</h3>
        {keys.total > 0 ? <Badge>{keys.total} runtime {keys.total === 1 ? "key" : "keys"}</Badge> : confirmed ? <Badge>No keys required — confirmed</Badge> : <Badge>Needs review</Badge>}
      </div>

      {keys.total === 0 && (
        <div className="mt-3 border border-status-warning/40 bg-status-warning/10 p-3">
          <PlanCheck checked={confirmed} label="This application does not require environment variables or secrets." onChange={(checked) => update((draft) => {
            clearConfirmation(draft);
            if (checked) draft.application_environment_reviews = [...(draft.application_environment_reviews || []), { application_source_key: application.source_key, no_environment_required: true }];
          })} />
          {!confirmed && <p className="mt-1 text-xs text-status-warning" role="status">Add a runtime key or explicitly confirm that this application needs none.</p>}
        </div>
      )}

      <section aria-labelledby={`plain-heading-${applicationIndex}`} className="mt-4 border border-outline-variant/20 bg-surface-container p-3 sm:p-4">
        <div className="flex items-center justify-between"><h4 className="text-sm font-medium" id={`plain-heading-${applicationIndex}`}>Plain variables ({keys.plain.length})</h4><span className="text-xs text-on-surface-variant">Maximum 64</span></div>
        {keys.plain.length === 0 ? <p className="mt-2 text-xs text-on-surface-variant">No plain variables configured.</p> : <ul className="mt-3 divide-y divide-outline-variant/20">{keys.plain.map(({ name, value }) => <li className="py-3 first:pt-0" key={name}>
          {renaming === name ? <div className="flex flex-col gap-2 sm:flex-row"><Input aria-label={`Rename ${name}`} className="font-mono text-xs" onChange={(event) => { setRenamedName(event.target.value); setError(""); }} value={renamedName} /><Button onClick={() => renamePlain(name)} size="sm" type="button" variant="secondary">Save name</Button><Button onClick={() => { setRenaming(""); setError(""); }} size="sm" type="button" variant="ghost">Cancel</Button></div> : <div className="grid gap-2 sm:grid-cols-[minmax(10rem,0.7fr)_minmax(12rem,1fr)_auto]"><div className="flex items-center gap-2"><code className="text-xs font-semibold">{name}</code>{canEdit && <Button onClick={() => { setRenaming(name); setRenamedName(name); setError(""); }} size="sm" type="button" variant="ghost">Rename</Button>}</div>{canEdit ? <Input aria-label={`Value for ${name}`} className="font-mono text-xs" onChange={(event) => {
            const next = event.target.value;
            if (next.length > 4096 || /[\x00\r\n]/.test(next)) return setError("Environment values must be at most 4096 characters and cannot contain NUL or line breaks.");
            setError("");
            update((draft) => { draft.applications[applicationIndex].environment![name] = next; });
          }} value={value} /> : <span className="break-all font-mono text-xs text-on-surface-variant">{value}</span>}{canEdit && <Button onClick={() => update((draft) => { delete draft.applications[applicationIndex].environment?.[name]; clearConfirmationIfEmpty(draft); })} size="sm" type="button" variant="ghost"><Icon name="delete" />Remove</Button>}</div>}
        </li>)}</ul>}
        {canEdit && <div className="mt-3 border-t border-outline-variant/20 pt-3"><p className="text-xs font-medium text-on-surface-variant">Add plain variable</p><div className="mt-2 grid gap-2 sm:grid-cols-[minmax(10rem,0.7fr)_minmax(12rem,1fr)_auto]"><Input aria-label="Variable key" className="font-mono text-xs" onChange={(event) => { setNewName(event.target.value); setError(""); }} placeholder="VARIABLE_NAME" value={newName} /><Input aria-label="Variable value" className="font-mono text-xs" onChange={(event) => { setNewValue(event.target.value); setError(""); }} placeholder="value" value={newValue} /><Button disabled={!newName.trim()} onClick={addPlain} size="sm" type="button" variant="secondary"><Icon name="add" />Add variable</Button></div></div>}
        {error && <p className="mt-3 text-sm text-error" role="alert">{error}</p>}
      </section>

      <SecretConfigurationEditor applicationID={applicationID} applicationKey={application.key} canEdit={canEdit} onAdd={(secret) => update((draft) => { draft.secrets.push(secret); clearConfirmation(draft); })} onList={listSecrets || noStoredSecrets} onRemove={(logicalName) => update((draft) => { draft.secrets = draft.secrets.filter((secret) => !(secret.application_key === application.key && secret.name === logicalName)); clearConfirmationIfEmpty(draft); })} onResolve={resolveSecret} onUpdate={(logicalName, metadata) => update((draft) => { const secret = draft.secrets.find((item) => item.application_key === application.key && item.name === logicalName); if (secret) { secret.secret_ref = metadata.reference; secret.revision = metadata.revision; secret.display = "Securely stored"; } })} reservedEnvironmentNames={reservedForSecrets} secrets={applicationSecrets} />

      <section aria-labelledby={`generated-heading-${applicationIndex}`} className="mt-4 border border-outline-variant/20 bg-surface-container p-3 sm:p-4"><div className="flex flex-wrap items-center justify-between gap-2"><h4 className="flex items-center gap-2 text-sm font-medium" id={`generated-heading-${applicationIndex}`}><Icon name="hub" />Generated variables ({keys.generated.length})</h4><span className="text-xs text-on-surface-variant">Read-only from dependencies</span></div>{keys.generated.length === 0 ? <p className="mt-2 text-xs text-on-surface-variant">No dependency-generated variables.</p> : <ul className="mt-3 divide-y divide-outline-variant/20">{keys.generated.map((key) => <li className="flex items-center justify-between gap-2 py-2 first:pt-0" key={`${key.name}-${key.source}`}><code className="text-xs font-semibold">{key.name}</code><span className="text-xs text-on-surface-variant">From {key.source}</span></li>)}</ul>}</section>
    </section>
  );
}
