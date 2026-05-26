import { describe, it, expect } from "vitest";
import { zitadelConsoleUrl } from "./zitadelConsole";

describe("zitadelConsoleUrl", () => {
  it.each([
    [
      "https://idp.example",
      "org_1",
      "users",
      "https://idp.example/ui/console/users?org=org_1",
    ],
    [
      "https://idp.example/",
      "org_1",
      "users",
      "https://idp.example/ui/console/users?org=org_1",
    ],
    [
      "https://idp.example",
      "org_1",
      "idp",
      "https://idp.example/ui/console/org-settings?id=idp&org=org_1",
    ],
    [
      "https://idp.example",
      "org_1",
      "branding",
      "https://idp.example/ui/console/org-settings?id=branding&org=org_1",
    ],
    [
      "https://idp.example",
      "org_1",
      "login",
      "https://idp.example/ui/console/org-settings?id=login&org=org_1",
    ],
    [
      "https://idp.example",
      "org_1",
      "lockout",
      "https://idp.example/ui/console/org-settings?id=lockout&org=org_1",
    ],
    [
      "https://idp.example",
      "org_1",
      "profile",
      "https://idp.example/ui/console/users/me?org=org_1",
    ],
    [
      "https://idp.example",
      "",
      "users",
      "https://idp.example/ui/console/users",
    ],
    [
      "https://idp.example",
      "",
      "idp",
      "https://idp.example/ui/console/org-settings?id=idp",
    ],
    ["", "org_1", "users", ""],
    [
      "https://idp.example",
      "org/needs encoding",
      "users",
      "https://idp.example/ui/console/users?org=org%2Fneeds%20encoding",
    ],
  ])("issuer=%s org=%s view=%s", (issuer, org, view, expected) => {
    expect(zitadelConsoleUrl(issuer, org, view as never)).toBe(expected);
  });
});

