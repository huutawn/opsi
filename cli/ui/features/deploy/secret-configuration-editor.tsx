import { useEffect, useState } from "react";
import { Badge, Button, Icon, Input } from "@/components/ui/primitives";
import { PlanField, planSelectClass } from "@/features/deploy/plan-form-controls";
import { isValidEnvironmentName } from "@/features/deploy/runtime-config";
import type { DeploymentPlan, WorkloadSecretMetadata } from "@/lib/contracts/registry";

type PlanSecret = DeploymentPlan["secrets"][number];

type Props = {
  applicationID: string;
  applicationKey: string;
  canEdit: boolean;
  onAdd: (secret: PlanSecret) => void;
  onList: (applicationID: string) => Promise<WorkloadSecretMetadata[]>;
  onRemove: (logicalName: string) => void;
  onResolve: (applicationID: string, logicalName: string, value: string) => Promise<WorkloadSecretMetadata>;
  onUpdate: (logicalName: string, metadata: WorkloadSecretMetadata) => void;
  reservedEnvironmentNames: string[];
  secrets: PlanSecret[];
};

export function SecretConfigurationEditor(props: Props) {
  const { applicationID, onList } = props;
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<"create" | "attach">("create");
  const [environmentName, setEnvironmentName] = useState("");
  const [logicalName, setLogicalName] = useState("");
  const [value, setValue] = useState("");
  const [stored, setStored] = useState<WorkloadSecretMetadata[]>([]);
  const [selected, setSelected] = useState("");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open || mode !== "attach") return;
    let active = true;
    setLoading(true);
    setError("");
    void onList(applicationID).then((items) => {
      if (!active) return;
      setStored(items);
      setSelected((current) => current || items[0]?.logical_name || "");
    }).catch((cause) => {
      if (active) setError(`Stored secrets could not be loaded: ${(cause as Error).message}`);
    }).finally(() => {
      if (active) setLoading(false);
    });
    return () => { active = false; };
  }, [applicationID, mode, onList, open]);

  function validateEnvironmentName(): string {
    const name = environmentName.trim();
    if (!isValidEnvironmentName(name)) return "Environment key must start with a letter or underscore and contain only letters, numbers, and underscores.";
    if (props.reservedEnvironmentNames.includes(name)) return `Environment key “${name}” already exists in this application.`;
    return "";
  }

  function close() {
    setOpen(false);
    setEnvironmentName("");
    setLogicalName("");
    setValue("");
    setError("");
  }

  async function storeNew() {
    const nameError = validateEnvironmentName();
    const secretName = logicalName.trim();
    if (nameError) return setError(nameError);
    if (!secretName || secretName.length > 128 || /[\x00\r\n]/.test(secretName)) return setError("Logical secret name must be 1–128 characters without control characters.");
    if (props.secrets.some((secret) => secret.name === secretName)) return setError(`Logical secret “${secretName}” is already attached to this application.`);
    if (!value || value.length > 8192 || /[\x00\r\n]/.test(value)) return setError("Secret value must be 1–8192 characters without NUL or line breaks.");
    if (props.secrets.length >= 32) return setError("An application can attach at most 32 secrets.");

    const submitted = value;
    setValue("");
    setBusy(true);
    setError("");
    try {
      const metadata = await props.onResolve(props.applicationID, secretName, submitted);
      props.onAdd({
        name: secretName,
        application_key: props.applicationKey,
        environment_name: environmentName.trim(),
        generated: false,
        secret_ref: metadata.reference,
        revision: metadata.revision,
        display: "Securely stored",
        confidence: "high",
        reason: "Added during deployment review.",
        evidence: [],
      });
      close();
    } catch (cause) {
      setError(`${(cause as Error).message} Re-enter the value to retry; it was cleared locally.`);
    } finally {
      setBusy(false);
    }
  }

  function attachExisting() {
    const nameError = validateEnvironmentName();
    if (nameError) return setError(nameError);
    const metadata = stored.find((item) => item.logical_name === selected);
    if (!metadata) return setError("Select an available stored secret.");
    if (props.secrets.some((secret) => secret.name === metadata.logical_name)) return setError(`Logical secret “${metadata.logical_name}” is already attached to this application.`);
    if (props.secrets.length >= 32) return setError("An application can attach at most 32 secrets.");
    props.onAdd({
      name: metadata.logical_name,
      application_key: props.applicationKey,
      environment_name: environmentName.trim(),
      generated: false,
      secret_ref: metadata.reference,
      revision: metadata.revision,
      display: "Securely stored",
      confidence: "high",
      reason: "Attached during deployment review.",
      evidence: [],
    });
    close();
  }

  return (
    <section className="mt-4 border border-outline-variant/20 bg-surface-container p-3 sm:p-4" aria-labelledby={`secrets-${props.applicationKey}`}>
      <div className="flex items-center justify-between gap-3">
        <h4 className="flex items-center gap-2 text-sm font-medium" id={`secrets-${props.applicationKey}`}><Icon name="security" />Secrets ({props.secrets.length})</h4>
        {props.canEdit && <Button onClick={() => open ? close() : setOpen(true)} size="sm" type="button" variant="outline"><Icon name={open ? "close" : "add"} />{open ? "Close" : "Add secret"}</Button>}
      </div>

      {props.secrets.length === 0 ? <p className="mt-2 text-xs text-on-surface-variant">No secrets attached to this application.</p> : (
        <ul className="mt-3 divide-y divide-outline-variant/20">
          {props.secrets.map((secret) => <SecretRow applicationID={props.applicationID} canEdit={props.canEdit} key={`${secret.name}-${secret.environment_name}`} onRemove={() => props.onRemove(secret.name)} onResolve={props.onResolve} onUpdate={(metadata) => props.onUpdate(secret.name, metadata)} secret={secret} />)}
        </ul>
      )}

      {open && props.canEdit && (
        <div className="mt-4 border border-outline-variant/30 bg-surface-container-low p-3 sm:p-4">
          <fieldset>
            <legend className="text-sm font-medium">Secret source</legend>
            <div className="mt-2 flex flex-wrap gap-4">
              <label className="flex min-h-10 cursor-pointer items-center gap-2 text-sm"><input checked={mode === "create"} name={`secret-mode-${props.applicationKey}`} onChange={() => setMode("create")} type="radio" />Store new secret</label>
              <label className="flex min-h-10 cursor-pointer items-center gap-2 text-sm"><input checked={mode === "attach"} name={`secret-mode-${props.applicationKey}`} onChange={() => setMode("attach")} type="radio" />Attach stored secret</label>
            </div>
          </fieldset>
          <div className="mt-3 grid gap-3 sm:grid-cols-2">
            <PlanField label="Environment key"><Input className="font-mono text-xs" onChange={(event) => { setEnvironmentName(event.target.value); setError(""); }} placeholder="STRIPE_API_KEY" value={environmentName} /></PlanField>
            {mode === "create" ? <PlanField label="Logical secret name"><Input className="font-mono text-xs" onChange={(event) => { setLogicalName(event.target.value); setError(""); }} placeholder="stripe-api-key" value={logicalName} /></PlanField> : <PlanField label="Stored secret"><select aria-busy={loading} className={planSelectClass} disabled={loading || stored.length === 0} onChange={(event) => setSelected(event.target.value)} value={selected}>{loading ? <option>Loading…</option> : stored.length === 0 ? <option value="">No stored secrets</option> : stored.map((item) => <option key={item.logical_name} value={item.logical_name}>{item.logical_name} · revision {item.revision}</option>)}</select></PlanField>}
          </div>
          {mode === "create" && <div className="mt-3"><PlanField label="Secret value"><Input autoComplete="new-password" disabled={busy} onChange={(event) => { setValue(event.target.value); setError(""); }} placeholder="Enter once; the value is never returned" type="password" value={value} /></PlanField></div>}
          {error && <p className="mt-3 text-sm text-error" role="alert">{error}</p>}
          <div className="mt-4 flex justify-end gap-2"><Button onClick={close} size="sm" type="button" variant="ghost">Cancel</Button><Button disabled={busy || !environmentName.trim() || (mode === "create" ? !logicalName.trim() || !value : !selected)} onClick={() => mode === "create" ? void storeNew() : attachExisting()} size="sm" type="button" variant="secondary">{busy ? "Storing…" : mode === "create" ? "Store securely" : "Attach secret"}</Button></div>
        </div>
      )}
    </section>
  );
}

function SecretRow({ applicationID, canEdit, onRemove, onResolve, onUpdate, secret }: { applicationID: string; canEdit: boolean; onRemove: () => void; onResolve: Props["onResolve"]; onUpdate: (metadata: WorkloadSecretMetadata) => void; secret: PlanSecret }) {
  const [rotating, setRotating] = useState(false);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const stored = Boolean(secret.secret_ref && secret.revision);
  const editing = !stored || rotating;

  async function rotate() {
    if (!value || value.length > 8192 || /[\x00\r\n]/.test(value)) return setError("Secret value must be 1–8192 characters without NUL or line breaks.");
    const submitted = value;
    setValue("");
    setBusy(true);
    setError("");
    try {
      onUpdate(await onResolve(applicationID, secret.name, submitted));
      setRotating(false);
    } catch (cause) {
      setError(`${(cause as Error).message} Re-enter the value to retry; it was cleared locally.`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <li className="py-3 first:pt-0">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div><code className="text-sm font-semibold">{secret.environment_name}</code><p className="mt-1 text-xs text-on-surface-variant">{secret.name} · {secret.generated ? "Generated and securely stored" : stored ? `Stored securely · revision ${secret.revision}` : "Value required; plaintext is never returned"}</p></div>
        {canEdit && !secret.generated && <div className="flex flex-wrap gap-2">{editing ? <><Input aria-label={`Value for ${secret.name}`} autoComplete="new-password" disabled={busy} onChange={(event) => setValue(event.target.value)} type="password" value={value} /><Button disabled={busy || !value} onClick={() => void rotate()} size="sm" type="button" variant="secondary">{busy ? "Storing…" : stored ? "Save revision" : "Store securely"}</Button>{stored && <Button onClick={() => { setRotating(false); setValue(""); setError(""); }} size="sm" type="button" variant="ghost">Cancel</Button>}</> : <><Button onClick={() => setRotating(true)} size="sm" type="button" variant="outline"><Icon name="rotate_right" />Rotate</Button><Button onClick={onRemove} size="sm" type="button" variant="ghost"><Icon name="delete" />Remove</Button></>}</div>}
      </div>
      {error && <p className="mt-2 text-sm text-error" role="alert">{error}</p>}
      {!secret.generated && canEdit && <p className="mt-2 text-xs text-on-surface-variant">Removing detaches this secret from the deployment plan; it does not delete the stored secret.</p>}
      {secret.generated && <Badge className="mt-2">Generated</Badge>}
    </li>
  );
}
