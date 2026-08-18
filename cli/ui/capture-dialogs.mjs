import { chromium } from "playwright";

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const page = await context.newPage();

  console.log("1. Testing Add Resource Dialog...");
  await page.goto("http://127.0.0.1:9780/?project=proj-b1b9ba6457f59185&view=infrastructure&tab=resources", { waitUntil: "networkidle" });
  await page.waitForTimeout(1000);
  const addResBtn = page.getByRole("button", { name: /Provision|Add/i }).first();
  if (await addResBtn.isVisible()) {
    await addResBtn.click();
    await page.waitForTimeout(600);
    await page.screenshot({ path: "/home/tawn/code/opsi/.tmp/ui-v2/live/dialog-add-resource.png" });
    console.log("Captured dialog-add-resource.png");
    
    // Also select Postgres to test form input view
    const pgCard = page.getByRole("button", { name: /PostgreSQL/i }).first();
    if (await pgCard.isVisible()) {
      await pgCard.click();
      await page.waitForTimeout(600);
      await page.screenshot({ path: "/home/tawn/code/opsi/.tmp/ui-v2/live/dialog-add-resource-form.png" });
      console.log("Captured dialog-add-resource-form.png");
    }
  }

  console.log("2. Testing Add Application Wizard Dialog in Services...");
  await page.goto("http://127.0.0.1:9780/?project=proj-b1b9ba6457f59185&view=services", { waitUntil: "networkidle" });
  await page.waitForTimeout(1000);
  const addAppBtn = page.getByRole("button", { name: /Add Application|New Application/i }).first();
  if (await addAppBtn.isVisible()) {
    await addAppBtn.click();
    await page.waitForTimeout(600);
    await page.screenshot({ path: "/home/tawn/code/opsi/.tmp/ui-v2/live/dialog-add-application.png" });
    console.log("Captured dialog-add-application.png");
  }

  await browser.close();
}

main().catch(console.error);
