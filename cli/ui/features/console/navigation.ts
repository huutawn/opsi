export const navGroups = [
  ["Setup", "Projects", "Servers / Nodes", "GitHub"],
  ["Operate", "Overview", "Services", "Deployments", "Incidents"],
  ["Understand", "Topology", "Logs", "Metrics", "Support"],
  ["Control", "Secrets", "Audit", "Settings"],
] as const;

export type ConsoleView = (typeof navGroups)[number][number] | "Projects";
