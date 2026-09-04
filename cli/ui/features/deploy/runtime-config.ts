import type { DeploymentPlan } from "@/lib/contracts/registry";

export function isSecretLikeEnvironmentName(value: string): boolean {
  const normalized = value.toLowerCase();
  const parts = normalized.split("_");
  for (const part of parts) {
    if (["password", "passwd", "secret", "token", "credential"].includes(part)) {
      return true;
    }
  }
  const compact = normalized.replaceAll("_", "");
  if (compact.includes("connectionstring")) {
    return true;
  }
  for (const suffix of ["password", "passwd", "secret", "token", "credential", "privatekey", "signingkey", "accesskey", "apikey", "key"]) {
    if (compact.endsWith(suffix)) {
      return true;
    }
  }
  return false;
}

export function isValidEnvironmentName(name: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(name);
}

export function getApplicationEffectiveKeys(plan: DeploymentPlan, app: DeploymentPlan["applications"][number]) {
  const plain: Array<{ name: string; value: string }> = [];
  if (app.environment) {
    for (const [name, value] of Object.entries(app.environment)) {
      if (name.trim()) plain.push({ name, value });
    }
  }
  const secrets = (plan.secrets || []).filter((s) => s.application_key === app.key && s.environment_name?.trim());
  const generated: Array<{ name: string; source: string }> = [];
  for (const dep of plan.dependencies || []) {
    if (dep.from === app.key && dep.injections) {
      for (const inj of dep.injections) {
        if (inj.environment_name?.trim()) {
          generated.push({ name: inj.environment_name, source: dep.to });
        }
      }
    }
  }
  return {
    plain,
    secrets,
    generated,
    total: plain.length + secrets.length + generated.length,
  };
}

export function isApplicationConfirmed(plan: DeploymentPlan, app: DeploymentPlan["applications"][number]): boolean {
  return (plan.application_environment_reviews || []).some(
    (r) => r.application_source_key === app.source_key && r.no_environment_required
  );
}

export function isApplicationReviewed(plan: DeploymentPlan, app: DeploymentPlan["applications"][number]): boolean {
  const keys = getApplicationEffectiveKeys(plan, app);
  return keys.total > 0 || isApplicationConfirmed(plan, app);
}

export function getUnreviewedApplications(plan: DeploymentPlan): DeploymentPlan["applications"] {
  return (plan.applications || []).filter((app) => !isApplicationReviewed(plan, app));
}
