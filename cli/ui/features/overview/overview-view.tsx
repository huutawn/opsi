"use client";

import { Empty, PageHeader, StatusBadge } from "@/components/ui/primitives";
import { routeHref } from "@/features/console/navigation";
import type { ConsoleController } from "@/features/console/types";
import { deliveryActivity, deriveProjectSummary, formatTimestamp, serviceRows, statusLabel } from "@/lib/presentation/project";

export function OverviewView({ console }: { console: ConsoleController }) {
  const project = console.state.project;
  if (!project) return <Empty title="Select a project" text="Choose a project from the workspace to see operational evidence." />;
  const projectID = project.id;
  const summary = deriveProjectSummary({ project, readiness: console.state.readiness, services: console.state.services, deployments: console.state.deployments, foundation: console.state.foundation });
  const rows = serviceRows({ services: console.state.services, telemetry: console.state.foundation.telemetry, telemetrySource: console.state.foundation.sources.telemetry, deployments: console.state.deployments, placement: console.state.foundation.placement, topology: console.state.foundation.topology });
  const activity = deliveryActivity(console.state.deployments);
  const healthyNodes = console.state.foundation.placement?.nodes.filter((node) => node.status === "healthy").length;
  const totalNodes = console.state.foundation.placement?.nodes.length;

  function follow(event: React.MouseEvent<HTMLAnchorElement>, target: { view: "delivery" | "services" | "infrastructure" | "observability"; tab?: string }) {
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    console.navigate({ projectID, view: target.view, tab: target.tab });
  }

  return (
    <section className="page overviewPage">
      <PageHeader eyebrow="Project overview" title={project.name} description="A concise view of factual delivery, runtime, and service health." action={<button aria-label="Refresh project overview" className="secondaryAction" onClick={() => void console.actions.load()} type="button">Refresh</button>} />
      <div className="statusStrip" aria-label="Project status summary">
        <div className={`statusLead ${summary.overall}`}><span>Status</span><strong>{statusLabel(summary.overall)}</strong><small>{summary.attention.length ? `${summary.attention.length} item${summary.attention.length === 1 ? "" : "s"} need attention` : "No current attention items"}</small></div>
        <div><span>Readiness</span><strong>{summary.readiness.desired ? `${summary.readiness.ready}/${summary.readiness.desired}` : "Not reported"}</strong><small>{summary.readiness.desired ? "ready pods" : "Telemetry source missing"}</small></div>
        <div><span>Latest delivery</span><strong>{summary.latestBuild ? <StatusBadge value={summary.latestBuild.build.status} /> : summary.latestDeployment ? <StatusBadge value={summary.latestDeployment.status} /> : "No data yet"}</strong><small>{summary.latestBuild ? summary.latestBuild.service_key : "Build source not reported"}</small></div>
        <div><span>Open incidents</span><strong>{console.state.foundation.sources.incidents === "available" ? summary.openIncidents : console.state.foundation.sources.incidents === "unavailable" ? "Unavailable" : "Not reported"}</strong><small>{console.state.foundation.sources.incidents !== "available" ? "Incident source missing" : summary.openIncidents ? "Needs investigation" : "No open incident reported"}</small></div>
        <div><span>Runtime</span><strong>{healthyNodes !== undefined && totalNodes !== undefined ? `${healthyNodes}/${totalNodes}` : "Unknown"}</strong><small>{console.state.foundation.sources.runtime === "available" ? "nodes healthy" : "Runtime source unavailable"}</small></div>
      </div>

      <div className="overviewGrid">
        <section className="sectionBlock deliveryActivity" aria-labelledby="deliveryActivityTitle">
          <div className="sectionHeading"><div><p className="eyebrow">Delivery</p><h2 id="deliveryActivityTitle">Delivery activity</h2></div><a href={routeHref({ projectID: project.id, view: "delivery", tab: "deployments" })} onClick={(event) => follow(event, { view: "delivery", tab: "deployments" })}>Open delivery</a></div>
          {activity.kind === "chart" ? <ActivityChart buckets={activity.buckets} /> : activity.events.length ? <div className="activityTimeline">{activity.events.slice(-6).reverse().map((deployment) => <div className="activityEvent" key={deployment.id}><span className="timelineDot" /><div><strong>{deployment.service_id}</strong><small>{deployment.rollout_state ?? deployment.status} · {formatTimestamp(deployment.updated_at ?? deployment.created_at)}</small></div><StatusBadge value={deployment.rollout_state ?? deployment.status} /></div>)}</div> : <Empty title="No delivery data yet" text="Accepted builds and deployments will appear here when the Local API reports them." />}
        </section>

        <section className="sectionBlock serviceHealth" aria-labelledby="serviceHealthTitle">
          <div className="sectionHeading"><div><p className="eyebrow">Runtime</p><h2 id="serviceHealthTitle">Service health</h2></div><a href={routeHref({ projectID: project.id, view: "services" })} onClick={(event) => follow(event, { view: "services" })}>View services</a></div>
          {rows.length ? <div className="healthList">{rows.slice(0, 6).map((row) => <a href={routeHref({ projectID: project.id, view: "services", service: row.service.id })} className="healthRow" key={row.service.id} onClick={(event) => { if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return; event.preventDefault(); console.navigate({ projectID: project.id, view: "services", service: row.service.id }); console.setServiceDetail(row.service); }}><span><strong>{row.service.name}</strong><small>{row.environment || "Environment not reported"} · {row.runtime || "Runtime not reported"}</small></span><span>{row.ready !== undefined && row.desired !== undefined ? `${row.ready}/${row.desired}` : "Not reported"}</span><StatusBadge label={statusLabel(row.health)} value={row.health} /></a>)}</div> : <Empty title="No services yet" text="The Local API has not reported a service catalog for this project." />}
        </section>
      </div>

      <div className="overviewGrid lower">
        <section className="sectionBlock" aria-labelledby="attentionTitle"><div className="sectionHeading"><div><p className="eyebrow">Next</p><h2 id="attentionTitle">Attention queue</h2></div></div>{summary.attention.length ? <div className="attentionList">{summary.attention.map((item) => <a className={`attentionItem ${item.status}`} href={routeHref({ projectID: project.id, view: item.target.view, tab: item.target.tab })} key={item.id} onClick={(event) => follow(event, item.target)}><span className="attentionMark" aria-hidden="true" /><span><strong>{item.title}</strong><small>{item.detail}</small></span><span aria-hidden="true">→</span></a>)}</div> : <Empty title="Nothing needs attention" text="No factual failure, mismatch, open incident, or unavailable source is reported." />}</section>
        <section className="sectionBlock" aria-labelledby="recentDeploymentsTitle"><div className="sectionHeading"><div><p className="eyebrow">History</p><h2 id="recentDeploymentsTitle">Recent deployments</h2></div><a href={routeHref({ projectID: project.id, view: "delivery", tab: "deployments" })} onClick={(event) => follow(event, { view: "delivery", tab: "deployments" })}>See all</a></div>{console.state.deployments.length ? <div className="deploymentList">{console.state.deployments.slice(0, 5).map((deployment) => <div className="deploymentRow" key={deployment.id}><span><strong>{deployment.service_id}</strong><small>{deployment.id} · {formatTimestamp(deployment.created_at)}</small></span><StatusBadge value={deployment.rollout_state ?? deployment.status} /></div>)}</div> : <Empty title="No deployments yet" text="Deployment history is empty for this project." />}</section>
      </div>
    </section>
  );
}

function ActivityChart({ buckets }: { buckets: Array<{ day: string; succeeded: number; failed: number; rolled_back: number; cancelled: number; other: number }> }) {
  const max = Math.max(...buckets.map((bucket) => bucket.succeeded + bucket.failed + bucket.rolled_back + bucket.cancelled + bucket.other), 1);
  const totals = buckets.reduce((sum, bucket) => ({ succeeded: sum.succeeded + bucket.succeeded, failed: sum.failed + bucket.failed, rolled_back: sum.rolled_back + bucket.rolled_back, cancelled: sum.cancelled + bucket.cancelled, other: sum.other + bucket.other }), { succeeded: 0, failed: 0, rolled_back: 0, cancelled: 0, other: 0 });
  return <figure className="activityChart" aria-labelledby="activityChartTitle"><figcaption><h3 className="srOnly" id="activityChartTitle">Delivery outcomes by day</h3><p className="chartSummary">{buckets.length} factual days · {Object.values(totals).reduce((sum, value) => sum + value, 0)} outcomes: {totals.succeeded} succeeded, {totals.failed} failed, {totals.rolled_back} rolled back, {totals.cancelled} cancelled, {totals.other} other.</p></figcaption><div className="chartLegend" aria-label="Outcome legend"><span><i className="legendSucceeded" />Succeeded</span><span><i className="legendFailed" />Failed</span><span><i className="legendRollback" />Rolled back</span><span><i className="legendCancelled" />Cancelled</span><span><i className="legendOther" />Other</span></div><div aria-hidden="true" className="barChart">{buckets.map((bucket) => <div className="barColumn" key={bucket.day}><div className="barStack"><i className="barSucceeded" style={{ height: `${(bucket.succeeded / max) * 100}%` }} /><i className="barFailed" style={{ height: `${(bucket.failed / max) * 100}%` }} /><i className="barRollback" style={{ height: `${(bucket.rolled_back / max) * 100}%` }} /><i className="barCancelled" style={{ height: `${(bucket.cancelled / max) * 100}%` }} /><i className="barOther" style={{ height: `${(bucket.other / max) * 100}%` }} /></div><small>{bucket.day.slice(5)}</small></div>)}</div><div className="chartTable"><table><caption>Delivery outcome data</caption><thead><tr><th scope="col">Day</th><th scope="col">Succeeded</th><th scope="col">Failed</th><th scope="col">Rolled back</th><th scope="col">Cancelled</th><th scope="col">Other</th><th scope="col">Total</th></tr></thead><tbody>{buckets.map((bucket) => <tr key={bucket.day}><th scope="row">{bucket.day}</th><td>{bucket.succeeded}</td><td>{bucket.failed}</td><td>{bucket.rolled_back}</td><td>{bucket.cancelled}</td><td>{bucket.other}</td><td>{bucket.succeeded + bucket.failed + bucket.rolled_back + bucket.cancelled + bucket.other}</td></tr>)}</tbody></table></div></figure>;
}
