import { describe, it, expect } from "vitest";
import { mount, flushPromises } from "@vue/test-utils";
import RedirectURIAllowlistEditor from "./RedirectURIAllowlistEditor.vue";

function mountEditor(initial: string[] = []) {
  return mount(RedirectURIAllowlistEditor, {
    props: {
      modelValue: initial,
      "onUpdate:modelValue": () => {},
      "onValidity-change": () => {},
    },
  });
}

describe("RedirectURIAllowlistEditor", () => {
  it("renders the empty-state message when no entries", () => {
    const w = mountEditor([]);
    expect(w.text()).toContain("No allowlist entries");
    expect(w.findAll('[data-testid^="allowlist-row-"]').length).toBe(0);
  });

  it("emits update:modelValue with the trimmed entry on input", async () => {
    const w = mountEditor([""]);
    const inputs = w.findAll("input[type=text]");
    expect(inputs.length).toBe(1);
    await inputs[0].setValue("https://app.acme.com/cb");
    const events = w.emitted("update:modelValue") ?? [];
    expect(events.at(-1)).toEqual([["https://app.acme.com/cb"]]);
  });

  it("shows a per-row validation error for an invalid pattern", async () => {
    const w = mountEditor([""]);
    await w.find("input").setValue("no-scheme");
    await flushPromises();
    const err = w.find(".text-error");
    expect(err.exists()).toBe(true);
    expect(err.text()).toContain("scheme://");
  });

  it("flips validity-change to false then back to true", async () => {
    const w = mountEditor([]);
    // The component emits validity-change on mount (initially true: empty list).
    const initial = w.emitted("validity-change") ?? [];
    expect(initial.at(-1)).toEqual([true]);

    // Add an empty entry → invalid (empty rows are invalid).
    await w.find('[data-testid="allowlist-add"]').trigger("click");
    await flushPromises();
    let events = w.emitted("validity-change") ?? [];
    expect(events.at(-1)).toEqual([false]);

    // Fill it in with a good pattern → valid again.
    await w.find("input").setValue("https://app.acme.com/cb");
    await flushPromises();
    events = w.emitted("validity-change") ?? [];
    expect(events.at(-1)).toEqual([true]);
  });

  it("removes an entry when the trash button is clicked", async () => {
    const w = mountEditor(["https://a.acme.com/cb", "https://b.acme.com/cb"]);
    await flushPromises();
    expect(w.findAll('[data-testid^="allowlist-row-"]').length).toBe(2);
    await w.find('[aria-label="Remove entry 1"]').trigger("click");
    await flushPromises();
    const events = w.emitted("update:modelValue") ?? [];
    expect(events.at(-1)).toEqual([["https://b.acme.com/cb"]]);
  });
});
