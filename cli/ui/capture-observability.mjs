import { chromium } from "playwright";
import { mkdir } from "node:fs/promises";

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();

  await mkdir("/home/tawn/code/opsi/.tmp/ui-v2/live", { recursive: true });

  const projectID = "proj-b1b9ba6457f59185";
  const tabs = [
    { name: "overview", url: `http://127.0.0.1:9780/?project=${projectID}&view=observability` },
    { name: "applications", url: `http://127.0.0.1:9780/?project=${projectID}&view=observability&tab=applications` },
    { name: "servers", url: `http://127.0.0.1:9780/?project=${projectID}&view=observability&tab=servers` },
    { name: "resources", url: `http://127.0.0.1:9780/?project=${projectID}&view=observability&tab=resources` },
    { name: "health", url: `http://127.0.0.1:9780/?project=${projectID}&view=observability&tab=health` },
    { name: "metrics", url: `http://127.0.0.1:9780/?project=${projectID}&view=observability&tab=metrics` },
    { name: "logs", url: `http://127.0.0.1:9780/?project=${projectID}&view=observability&tab=logs` },
    { name: "incidents", url: `http://127.0.0.1:9780/?project=${projectID}&view=observability&tab=incidents` },
  ];

  for (const tab of tabs) {
    console.log(`Navigating to Observability ${tab.name}...`);
    await page.goto(tab.url, { waitUntil: "networkidle" });
    await page.waitForTimeout(800);
    await page.screenshot({ path: `/home/tawn/code/opsi/.tmp/ui-v2/live/observability-${tab.name}.png` });
    console.log(`Captured observability-${tab.name}.png`);
  }

  await browser.close();
}

main().catch(console.error);
