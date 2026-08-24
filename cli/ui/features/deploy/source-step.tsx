import { Button, Input, Select } from "@/components/ui/primitives";
import type { GitHubRepository } from "@/lib/contracts/registry";

export function SourceStep({ busy, canMutate, hostname, onHostname, onRepository, onStart, onRef, refName, repositories, repositoryID }: { busy: boolean; canMutate: boolean; hostname: string; onHostname: (value: string) => void; onRepository: (value: number) => void; onStart: () => void; onRef: (value: string) => void; refName: string; repositories: GitHubRepository[]; repositoryID: number }) {
  const selected = repositories.find((item) => item.repository_id === repositoryID);
  return (
    <section aria-labelledby="source-step-title" className="mx-auto max-w-2xl border border-outline-variant/30 bg-surface-container p-5 sm:p-7">
      <p className="text-xs font-medium uppercase tracking-wider text-secondary">Source</p>
      <h2 className="mt-2 text-2xl font-semibold" id="source-step-title">Choose a repository to deploy</h2>
      <p className="mt-2 text-sm text-on-surface-variant">Opsi reads one exact commit through your GitHub App installation. It does not write source, create a PR, or require repository workflows.</p>
      <div className="mt-6 space-y-4">
        <label className="block text-sm font-medium" htmlFor="source-repository">Repository</label>
        <Select disabled={busy || !canMutate} id="source-repository" onChange={(event) => { const id = Number(event.target.value); onRepository(id); const repository = repositories.find((item) => item.repository_id === id); if (repository) onRef(repository.default_branch || "main"); }} value={repositoryID || ""}><option value="">Select a claimed repository</option>{repositories.filter((item) => item.claim_status === "active").map((repository) => <option key={repository.repository_id} value={repository.repository_id}>{repository.full_name}</option>)}</Select>
        <div className="grid gap-4 sm:grid-cols-2"><label className="text-sm font-medium" htmlFor="source-ref">Branch or ref<Input disabled={busy || !canMutate} id="source-ref" onChange={(event) => onRef(event.target.value)} value={refName} /></label><label className="text-sm font-medium" htmlFor="source-hostname">Hostname (optional)<Input disabled={busy || !canMutate} id="source-hostname" onChange={(event) => onHostname(event.target.value)} placeholder="Generated from project domain" value={hostname} /></label></div>
      </div>
      {repositories.length === 0 && <p className="mt-5 border border-status-warning/40 bg-status-warning/10 p-3 text-sm">No GitHub repository is connected to this project. Connect and claim one in Settings → Integrations.</p>}
      <div className="mt-7 flex justify-end"><Button disabled={busy || !canMutate || !selected || !refName.trim()} onClick={onStart} size="lg"><span>{busy ? "Analyzing repository…" : "Analyze repository"}</span></Button></div>
    </section>
  );
}
