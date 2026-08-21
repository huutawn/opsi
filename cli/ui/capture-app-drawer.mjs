import { chromium } from "playwright";

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();

  const projectID = "proj-b1b9ba6457f59185";
  console.log("Navigating to Applications tab...");
  await page.goto(`http://127.0.0.1:9780/?project=${projectID}&view=observability&tab=applications`, { waitUntil: "networkidle" });
  await page.waitForTimeout(1000);

  const inspectBtn = page.getByRole("button", { name: /Inspect/i }).first();
  if (await inspectBtn.isVisible()) {
    await inspectBtn.click();
    await page.waitForTimeout(600);
    await page.screenshot({ path: `/home/tawn/code/opsi/.tmp/ui-v2/live/observability-app-drawer.png` });
    console.log("Captured observability-app-drawer.png");
  }

  await browser.close();
}

main().catch(console.error);
