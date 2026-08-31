"use client";

import { useEffect, useRef, useState } from "react";
import { Button, Icon } from "@/components/ui/primitives";
import type {
  AnalysisScope,
  RepositoryExportPreview,
  RepositoryExportResult,
} from "@/lib/contracts/registry";

export function RefineAnalysis({
  busy,
  initialScope,
  onRefine,
}: {
  busy: boolean;
  initialScope: AnalysisScope;
  onRefine: (scope: AnalysisScope) => void;
}) {
  const [roots, setRoots] = useState(initialScope.application_roots.join("\n"));
  const [excludes, setExcludes] = useState(
    initialScope.exclude_paths.join("\n"),
  );
  return (
    <section
      aria-labelledby="refine-analysis-title"
      className="border border-status-warning/40 bg-warning-container/10 p-4"
    >
      <div className="flex items-start gap-3">
        <Icon className="text-status-warning" name="filter_alt" />
        <div>
          <h2 className="font-semibold" id="refine-analysis-title">
            Refine analysis
          </h2>
          <p className="mt-1 text-sm text-on-surface-variant">
            Narrow heuristics on this exact commit. One repository-relative path
            per line.
          </p>
        </div>
      </div>
      <div className="mt-4 grid gap-4 md:grid-cols-2">
        <label className="text-sm font-medium">
          Application roots
          <textarea
            className="mt-1 min-h-24 w-full border border-outline-variant/40 bg-surface-container-low p-3 font-mono text-sm"
            onChange={(event) => setRoots(event.target.value)}
            placeholder={"be\ntcip-fake"}
            value={roots}
          />
        </label>
        <label className="text-sm font-medium">
          Exclude paths
          <textarea
            className="mt-1 min-h-24 w-full border border-outline-variant/40 bg-surface-container-low p-3 font-mono text-sm"
            onChange={(event) => setExcludes(event.target.value)}
            placeholder={"docs/archive\nexamples"}
            value={excludes}
          />
        </label>
      </div>
      <Button
        className="mt-4"
        disabled={busy}
        onClick={() =>
          onRefine({
            application_roots: paths(roots),
            exclude_paths: paths(excludes),
          })
        }
        variant="outline"
      >
        {busy ? "Refining…" : "Analyze exact commit with scope"}
      </Button>
    </section>
  );
}

export function RepositoryExport({
  canCreate,
  onCreate,
  onPreview,
  result,
}: {
  canCreate: boolean;
  onCreate: (preview: RepositoryExportPreview) => Promise<boolean>;
  onPreview: () => Promise<RepositoryExportPreview | null>;
  result: RepositoryExportResult | null;
}) {
  const [preview, setPreview] = useState<RepositoryExportPreview | null>(null);
  const [busy, setBusy] = useState("");
  const [createFailed, setCreateFailed] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (preview && !dialog.current?.open) dialog.current?.showModal();
  }, [preview]);
  const close = () => {
    dialog.current?.close();
    setPreview(null);
    window.requestAnimationFrame(() => trigger.current?.focus());
  };
  const open = async () => {
    setCreateFailed(false);
    setBusy("preview");
    const next = await onPreview();
    setBusy("");
    if (next) setPreview(next);
  };
  return (
    <section className="flex flex-col gap-3 border border-outline-variant/30 bg-surface-container-low p-4 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h2 className="font-semibold">Repository configuration</h2>
        <p className="mt-1 text-sm text-on-surface-variant">
          Optionally document this plan in a pull request. Analysis and
          deployment never write source.
        </p>
        {result && (
          <a
            className="mt-2 inline-block text-sm text-primary underline"
            href={result.pull_request_url}
            rel="noreferrer"
            target="_blank"
          >
            Open pull request #{result.pull_request_number}
          </a>
        )}
      </div>
      <Button
        disabled={Boolean(busy)}
        onClick={() => void open()}
        ref={trigger}
        variant="outline"
      >
        {busy === "preview" ? "Preparing preview…" : "Export configuration"}
      </Button>
      <dialog
        aria-describedby="repository-export-description"
        aria-labelledby="repository-export-title"
        className="fixed inset-0 z-50 m-auto max-h-[90vh] w-[min(56rem,calc(100%-2rem))] overflow-y-auto border border-outline-variant/40 bg-surface-container-low p-0 text-on-surface shadow-2xl backdrop:bg-background/80"
        onCancel={(event) => {
          event.preventDefault();
          close();
        }}
        ref={dialog}
      >
        <div className="flex items-center justify-between border-b border-outline-variant/30 p-4">
          <h2 className="font-semibold" id="repository-export-title">
            Review configuration export
          </h2>
          <Button onClick={close} variant="ghost">
            Close
          </Button>
        </div>
        {preview && (
          <div className="space-y-4 p-4">
            <p
              className="text-sm text-on-surface-variant"
              id="repository-export-description"
            >
              Target <strong>{preview.target_branch}</strong>. The pull request
              affects only future analyses and will not be merged automatically.
            </p>
            <pre
              aria-label="Repository configuration diff"
              className="max-h-80 overflow-auto border border-outline-variant/30 bg-surface p-3 text-xs whitespace-pre-wrap"
            >
              {preview.diff || "No repository changes."}
            </pre>
            {!preview.export_enabled && (
              <p className="text-sm text-error" role="status">
                {preview.disabled_reason}
              </p>
            )}
            {canCreate ? (
              <Button
                disabled={
                  busy === "create" || !preview.export_enabled || !preview.diff
                }
                onClick={async () => {
                  setBusy("create");
                  setCreateFailed(false);
                  const created = await onCreate(preview);
                  setCreateFailed(!created);
                  setBusy("");
                  if (created) close();
                }}
                variant="primary"
              >
                {busy === "create"
                  ? "Creating pull request…"
                  : "Create pull request"}
              </Button>
            ) : (
              <p className="text-sm text-on-surface-variant" role="status">
                Your role can review this preview. An operator or owner must
                create the pull request.
              </p>
            )}
            {createFailed && (
              <p className="text-sm text-error" role="alert">
                The pull request was not created. Review the error and try
                again.
              </p>
            )}
          </div>
        )}
      </dialog>
    </section>
  );
}

function paths(value: string) {
  return [
    ...new Set(
      value
        .split(/[\n,]/)
        .map((item) => item.trim())
        .filter(Boolean),
    ),
  ].sort();
}
