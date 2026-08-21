import { chromium } from "playwright";
import { mkdir } from "node:fs/promises";

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();

  await mkdir("/home/tawn/code/opsi/.tmp/ui-v2/live", { recursive: true });

  console.log("Navigating to Topology...");
  await page.goto("http://127.0.0.1:9780/?project=proj-b1b9ba6457f59185&view=topology", { waitUntil: "networkidle" });
  await page.waitForTimeout(1000);
  await page.screenshot({ path: "/home/tawn/code/opsi/.tmp/ui-v2/live/topology.png" });
  console.log("Captured topology.png");

  // Switch to Live mode via UI interaction
  const liveBtn = page.getByRole("button", { name: "Live" });
  if (await liveBtn.isVisible()) {
    await liveBtn.click();
    await page.waitForTimeout(1000);
    await page.screenshot({ path: "/home/tawn/code/opsi/.tmp/ui-v2/live/topology-live.png" });
    console.log("Captured topology-live.png via UI interaction");
  }

  // Switch to Services
  const servicesLink = page.getByRole("link", { name: /Services/i });
  if (await servicesLink.isVisible()) {
    await servicesLink.click();
    await page.waitForTimeout(1000);
    await page.screenshot({ path: "/home/tawn/code/opsi/.tmp/ui-v2/live/services.png" });
    console.log("Captured services.png");
  }

  await browser.close();
}

main().catch(console.error);
