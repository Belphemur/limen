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
      "project",
      "https://idp.example/ui/console/projects?org=org_1",
    ],
    [
      "https://idp.example",
      "org_1",
      "idp",
      "https://idp.example/ui/console/instance/idp?org=org_1",
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
