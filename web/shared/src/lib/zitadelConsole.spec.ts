import { describe, it, expect } from "vitest";
import { zitadelConsoleUrl, zitadelRoleAssignmentUrl } from "./zitadelConsole";

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

describe("zitadelRoleAssignmentUrl", () => {
  it("builds a granted-projects deep-link", () => {
    expect(
      zitadelRoleAssignmentUrl(
        "https://idp.example",
        "org_1",
        "proj_1",
        "grant_1",
      ),
    ).toBe(
      "https://idp.example/ui/console/granted-projects/proj_1/grant/grant_1?org=org_1",
    );
  });

  it("omits the org query when orgId is empty", () => {
    expect(
      zitadelRoleAssignmentUrl("https://idp.example", "", "proj_1", "grant_1"),
    ).toBe(
      "https://idp.example/ui/console/granted-projects/proj_1/grant/grant_1",
    );
  });

  it.each([
    ["", "org", "p", "g"],
    ["https://idp.example", "org", "", "g"],
    ["https://idp.example", "org", "p", ""],
  ])(
    "returns '' when a required argument is empty (%s,%s,%s,%s)",
    (issuer, org, project, grant) => {
      expect(zitadelRoleAssignmentUrl(issuer, org, project, grant)).toBe("");
    },
  );

  it("URL-encodes IDs", () => {
    expect(
      zitadelRoleAssignmentUrl(
        "https://idp.example/",
        "org/1",
        "proj 1",
        "grant 1",
      ),
    ).toBe(
      "https://idp.example/ui/console/granted-projects/proj%201/grant/grant%201?org=org%2F1",
    );
  });
});
