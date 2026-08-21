"use client";

import type { ConsoleController } from "@/features/console/types";
import { OverviewTab } from "@/features/security/overview-tab";
import { AuditTab } from "@/features/security/audit-tab";
import { AccessTab } from "@/features/security/access-tab";

export function SecurityView({ console }: { console: ConsoleController }) {
  const tab = console.route.tab;
  if (tab === "overview") {
    return <OverviewTab console={console} />;
  }
  if (tab === "audit") {
    return <AuditTab console={console} />;
  }
  if (tab === "access" || tab === "secrets") {
    return <AccessTab console={console} />;
  }
  return <OverviewTab console={console} />;
}
