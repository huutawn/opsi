import { expect, type Page, type Request } from "@playwright/test";

export type ExpectedHTTPFailure = {
  path: string;
  status: number;
  method?: string;
};

export type ExpectedRequestFailure = ExpectedHTTPFailure & { errorText: string };

type HTTPFailure = { url: string; status: number; method: string };
type RequestFailure = HTTPFailure & { errorText: string };
type ResourceError = { message: string; url: string };
type ErrorState = {
  errors: string[];
  expected: ExpectedHTTPFailure[];
  expectedRequests: ExpectedRequestFailure[];
  allowedResources: Set<string>;
  resources: ResourceError[];
  responseStatuses: WeakMap<Request, number>;
};

const states = new WeakMap<Page, ErrorState>();

export function matchesExpectedHTTPFailure(expected: ExpectedHTTPFailure, actual: HTTPFailure) {
  return expected.path === new URL(actual.url).pathname + new URL(actual.url).search
    && expected.status === actual.status
    && (!expected.method || expected.method === actual.method);
}

export function matchesExpectedRequestFailure(expected: ExpectedRequestFailure, actual: RequestFailure) {
  return matchesExpectedHTTPFailure(expected, actual) && expected.errorText === actual.errorText;
}

export function expectHTTPFailure(page: Page, expected: ExpectedHTTPFailure) {
  states.get(page)?.expected.push({ ...expected, method: expected.method?.toUpperCase() });
}

export function expectRequestFailure(page: Page, expected: ExpectedRequestFailure) {
  states.get(page)?.expectedRequests.push({ ...expected, method: expected.method?.toUpperCase() });
}

export function watchConsoleErrors(page: Page) {
  const state: ErrorState = { errors: [], expected: [], expectedRequests: [], allowedResources: new Set(), resources: [], responseStatuses: new WeakMap() };
  states.set(page, state);
  page.on("response", (response) => {
    state.responseStatuses.set(response.request(), response.status());
    if (response.status() < 400) return;
    const actual = { url: response.url(), status: response.status(), method: response.request().method() };
    if (state.expected.some((expected) => matchesExpectedHTTPFailure(expected, actual))) {
      state.allowedResources.add(actual.url);
      return;
    }
    state.errors.push(`HTTP ${actual.status} ${actual.method} ${actual.url}`);
  });
  page.on("requestfailed", (request) => {
    const actual = {
      url: request.url(),
      status: state.responseStatuses.get(request) ?? 0,
      method: request.method(),
      errorText: request.failure()?.errorText ?? "unknown error",
    };
    if (state.expectedRequests.some((expected) => matchesExpectedRequestFailure(expected, actual))) return;
    state.errors.push(`Request failed ${actual.method} ${actual.url}: ${actual.errorText}`);
  });
  page.on("console", (message) => {
    if (message.type() !== "error") return;
    const location = message.location().url;
    if (location && message.text().startsWith("Failed to load resource:")) {
      state.resources.push({ message: message.text(), url: location });
      return;
    }
    state.errors.push(`console.error: ${message.text()}`);
  });
  page.on("pageerror", (error) => state.errors.push(`pageerror: ${error.message}`));
}

export function collectedErrors(page: Page) {
  const state = states.get(page);
  if (!state) return [];
  return [
    ...state.errors,
    ...state.resources.filter((resource) => !state.allowedResources.has(resource.url)).map((resource) => `Resource error ${resource.url}: ${resource.message}`),
  ];
}

export function resetConsoleErrors(page: Page) {
  const state = states.get(page);
  if (!state) return;
  state.errors.length = 0;
  state.expected.length = 0;
  state.expectedRequests.length = 0;
  state.allowedResources.clear();
  state.resources.length = 0;
}

export function expectNoConsoleErrors(page: Page) {
  expect(collectedErrors(page)).toEqual([]);
}
