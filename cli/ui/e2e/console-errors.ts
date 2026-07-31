import { expect, type Page } from "@playwright/test";

const errors = new WeakMap<Page, string[]>();

export function watchConsoleErrors(page: Page) {
  const messages: string[] = [];
  errors.set(page, messages);
  page.on("console", (message) => {
    const expectedLocalFailure = message.text().startsWith("Failed to load resource:");
    if (message.type() === "error" && !expectedLocalFailure) messages.push(message.text());
  });
  page.on("pageerror", (error) => messages.push(error.message));
}

export function expectNoConsoleErrors(page: Page) {
  expect(errors.get(page) ?? []).toEqual([]);
}
