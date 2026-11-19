import { describe, expect, it } from "vitest";
import { date, maskIban, money, percent } from "./format";
import { rangeQuery } from "../components/RangePicker";

describe("format", () => {
  it("renders backend decimal strings without changing their value", () => {
    expect(money("1234.5", "EUR")).toMatch(/1.?234[.,]50/);
    expect(money(null, "EUR")).toBe("—");
    expect(percent("42.50")).toMatch(/^42[.,]5%$/);
  });

  it("never shifts civil dates across time zones", () => {
    expect(date("2026-01-01", { year: "numeric", month: "2-digit", day: "2-digit" })).toMatch(/01/);
    expect(date("2026-12-31", { day: "numeric" })).toBe("31");
  });

  it("masks IBANs", () => {
    expect(maskIban("IT60X0542811101000000123456")).toBe("IT60 •••• 3456");
  });
});

describe("rangeQuery", () => {
  it("passes preset ranges through", () => {
    expect(rangeQuery({ range: "1w" })).toEqual({ range: "1w" });
  });
  it("needs both dates for custom ranges", () => {
    expect(rangeQuery({ range: "custom", from: "2026-01-01" })).toEqual({ range: "1m" });
    expect(rangeQuery({ range: "custom", from: "2026-01-01", to: "2026-01-31" })).toEqual({ range: "custom", from: "2026-01-01", to: "2026-01-31" });
  });
});
