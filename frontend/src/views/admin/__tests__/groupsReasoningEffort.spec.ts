import { describe, expect, it } from "vitest";

import {
  createReasoningEffortMappingPair,
  createReasoningEffortMappingRow,
  normalizeReasoningEffortForPlatform,
  normalizeReasoningEffortSourceForPlatform,
  normalizeReasoningEffortMatchType,
  normalizeReasoningEffortOverLimit,
  reasoningEffortMappingsToAPI,
  reasoningEffortMappingsToRows,
  reasoningEffortOptionsForPlatform,
  reasoningEffortSourceOptionsForPlatform,
  reasoningEffortTargetOptionsForPlatform,
  reasoningEffortMappingDeny,
  reasoningEffortOverLimitDeny,
  reasoningEffortOverLimitDowngrade,
  normalizeReasoningEffortTargetForPlatform,
  supportsReasoningEffortPolicyPlatform,
  validateReasoningEffortMappings,
} from "../groupsReasoningEffort";

describe("groupsReasoningEffort", () => {
  it("provides fixed OpenAI choices to OpenAI and Composite groups", () => {
    const expected = [
      "minimal",
      "low",
      "medium",
      "high",
      "xhigh",
      "max",
    ];
    for (const platform of ["openai", "composite"] as const) {
      expect(
        reasoningEffortOptionsForPlatform(platform).map(
          (option) => option.value,
        ),
      ).toEqual(expected);
      expect(
        reasoningEffortSourceOptionsForPlatform(platform).map(
          (option) => option.value,
        ),
      ).toEqual(["none", ...expected]);
      expect(
        reasoningEffortTargetOptionsForPlatform(platform).map(
          (option) => option.value,
        ),
      ).toEqual([...expected, reasoningEffortMappingDeny]);
      expect(supportsReasoningEffortPolicyPlatform(platform)).toBe(true);
    }
    expect(
      reasoningEffortOptionsForPlatform("anthropic").map(
        (option) => option.value,
      ),
    ).toEqual(["low", "medium", "high", "xhigh", "max"]);
    expect(
      reasoningEffortTargetOptionsForPlatform("anthropic").map(
        (option) => option.value,
      ),
    ).toEqual(["low", "medium", "high", "xhigh", "max", reasoningEffortMappingDeny]);
    expect(supportsReasoningEffortPolicyPlatform("anthropic")).toBe(true);

    for (const platform of ["gemini", "antigravity", "grok"] as const) {
      expect(reasoningEffortOptionsForPlatform(platform)).toEqual([]);
      expect(reasoningEffortSourceOptionsForPlatform(platform)).toEqual([]);
      expect(reasoningEffortTargetOptionsForPlatform(platform)).toEqual([]);
      expect(supportsReasoningEffortPolicyPlatform(platform)).toBe(false);
    }
  });

  it("hydrates standard and model-scoped custom values without losing their type", () => {
    const rows = reasoningEffortMappingsToRows(
      [
        { from: " max ", to: " xhigh " },
        { from: "Custom_MODEL_Effort", to: "high", model: "gpt-example" },
        { from: "18446744073709551615", from_type: "number", to: "high", model: "gpt-example" },
      ],
      "openai",
    );

    expect(rows).toHaveLength(2);
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: "max", to: "xhigh" },
      { from: "Custom_MODEL_Effort", to: "high", model: "gpt-example", match_type: "exact" },
      { from: "18446744073709551615", from_type: "number", to: "high", model: "gpt-example", match_type: "exact" },
    ]);
    expect(validateReasoningEffortMappings(rows)).toEqual({});
  });

  it("hydrates and validates none only as a mapping source", () => {
    const rows = reasoningEffortMappingsToRows(
      [{ from: " NONE ", to: "low" }],
      "openai",
    );
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: "none", to: "low" },
    ]);
    expect(validateReasoningEffortMappings(rows, "openai")).toEqual({});
    expect(normalizeReasoningEffortSourceForPlatform("composite", " NONE ")).toBe("none");

    const invalidTarget = createReasoningEffortMappingRow({
      from: "low",
      to: "none",
    });
    expect(validateReasoningEffortMappings([invalidTarget], "openai")).toEqual({
      [invalidTarget.pairs[0].id]: { to: "unsupportedTo" },
    });
    expect(normalizeReasoningEffortForPlatform("openai", "none")).toBe("");
  });

  it("hydrates and validates deny only as a mapping target", () => {
    const rows = reasoningEffortMappingsToRows(
      [{ from: " xhigh ", to: " DENY " }],
      "openai",
    );
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: "xhigh", to: reasoningEffortMappingDeny },
    ]);
    expect(validateReasoningEffortMappings(rows, "openai")).toEqual({});
    expect(normalizeReasoningEffortTargetForPlatform("openai", " Deny ")).toBe(
      reasoningEffortMappingDeny,
    );
    expect(normalizeReasoningEffortForPlatform("openai", "deny")).toBe("");
    expect(normalizeReasoningEffortSourceForPlatform("openai", "deny")).toBe("");

    const invalidSource = createReasoningEffortMappingRow({
      from: "deny",
      to: "low",
    });
    expect(validateReasoningEffortMappings([invalidSource], "openai")).toEqual({
      [invalidSource.pairs[0].id]: { from: "sourceModelRequired" },
    });
  });

  it("hydrates model scoped mappings", () => {
    const rows = reasoningEffortMappingsToRows(
      [
        {
          from: "max",
          to: "low",
          match_type: "prefix",
          model: " gpt ",
        },
        {
          from: "max",
          to: "medium",
          match_type: "exact",
          model: "gpt-5.4",
        },
      ],
      "openai",
    );

    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: "max", to: "low", match_type: "prefix", model: "gpt" },
      { from: "max", to: "medium", match_type: "exact", model: "gpt-5.4" },
    ]);
  });

  it("groups multiple request mappings under the same type and model", () => {
    const rows = reasoningEffortMappingsToRows(
      [
        { from: "high", to: "medium", match_type: "prefix", model: "gpt" },
        { from: "xhigh", to: "medium", match_type: "prefix", model: "GPT" },
      ],
      "openai",
    );

    expect(rows).toHaveLength(1);
    expect(rows[0].match_type).toBe("prefix");
    expect(rows[0].model).toBe("gpt");
    expect(rows[0].pairs.map((pair) => pair.from)).toEqual(["high", "xhigh"]);
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: "high", to: "medium", match_type: "prefix", model: "gpt" },
      { from: "xhigh", to: "medium", match_type: "prefix", model: "gpt" },
    ]);
  });

  it("omits empty model scopes from the API payload", () => {
    const row = createReasoningEffortMappingRow({
      from: "max",
      to: "low",
      match_type: "prefix",
    });
    expect(reasoningEffortMappingsToAPI([row])).toEqual([
      { from: "max", to: "low" },
    ]);
  });

  it("hydrates suffix mappings", () => {
    const rows = reasoningEffortMappingsToRows(
      [{ from: "max", to: "low", match_type: "suffix", model: " mini " }],
      "openai",
    );
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: "max", to: "low", match_type: "suffix", model: "mini" },
    ]);
  });

  it("clears values unsupported by OpenAI or used on another platform", () => {
    expect(normalizeReasoningEffortForPlatform("openai", " MAX ")).toBe("max");
    expect(normalizeReasoningEffortForPlatform("composite", " MAX ")).toBe(
      "max",
    );
    expect(normalizeReasoningEffortForPlatform("anthropic", "xhigh")).toBe(
      "xhigh",
    );
    expect(normalizeReasoningEffortForPlatform("anthropic", "minimal")).toBe(
      "",
    );
    expect(normalizeReasoningEffortForPlatform("grok", "max")).toBe("");
    expect(normalizeReasoningEffortForPlatform("openai", "none")).toBe("");
  });

  it("normalizes match type to exact, prefix, suffix, or empty", () => {
    expect(normalizeReasoningEffortMatchType("PREFIX")).toBe("prefix");
    expect(normalizeReasoningEffortMatchType("suffix")).toBe("suffix");
    expect(normalizeReasoningEffortMatchType("exact")).toBe("exact");
    expect(normalizeReasoningEffortMatchType("")).toBe("");
    expect(normalizeReasoningEffortMatchType("wildcard")).toBe("");
  });

  it("requires both sides of every mapping", () => {
    const first = createReasoningEffortMappingRow({ to: "low" });
    const second = createReasoningEffortMappingRow({ from: "max" });

    expect(validateReasoningEffortMappings([first, second])).toEqual({
      [first.id]: { duplicateScope: "duplicateScope" },
      [second.id]: { duplicateScope: "duplicateScope" },
      [first.pairs[0].id]: { from: "fromRequired" },
      [second.pairs[0].id]: { to: "toRequired" },
    });
  });

  it("rejects duplicate source values case insensitively", () => {
    const first = createReasoningEffortMappingRow({ from: "MAX", to: "xhigh" });
    const second = createReasoningEffortMappingRow({ from: " max ", to: "high" });

    expect(validateReasoningEffortMappings([first, second])).toEqual({
      [first.id]: { duplicateScope: "duplicateScope" },
      [second.id]: { duplicateScope: "duplicateScope" },
      [first.pairs[0].id]: { from: "duplicateFrom" },
      [second.pairs[0].id]: { from: "duplicateFrom" },
    });
  });

  it("allows the same source across different model scopes", () => {
    const prefix = createReasoningEffortMappingRow({
      from: "max",
      to: "low",
      match_type: "prefix",
      model: "gpt",
    });
    const exact = createReasoningEffortMappingRow({
      from: "max",
      to: "medium",
      match_type: "exact",
      model: "gpt-5.4",
    });
    const global = createReasoningEffortMappingRow({ from: "max", to: "high" });

    expect(validateReasoningEffortMappings([prefix, exact, global])).toEqual({});
  });

  it("allows multiple request values in one model scope", () => {
    const row = createReasoningEffortMappingRow({
      from: "high",
      to: "medium",
      match_type: "prefix",
      model: "gpt",
    });
    row.pairs.push(
      createReasoningEffortMappingPair({ from: "xhigh", to: "medium" }),
    );

    expect(validateReasoningEffortMappings([row])).toEqual({});
    expect(reasoningEffortMappingsToAPI([row])).toEqual([
      { from: "high", to: "medium", match_type: "prefix", model: "gpt" },
      { from: "xhigh", to: "medium", match_type: "prefix", model: "gpt" },
    ]);
  });

  it("rejects duplicate source values within the same model scope", () => {
    const first = createReasoningEffortMappingRow({
      from: "max",
      to: "low",
      match_type: "prefix",
      model: "GPT",
    });
    const second = createReasoningEffortMappingRow({
      from: "MAX",
      to: "high",
      match_type: "prefix",
      model: " gpt ",
    });

    expect(validateReasoningEffortMappings([first, second])).toEqual({
      [first.id]: { duplicateScope: "duplicateScope" },
      [second.id]: { duplicateScope: "duplicateScope" },
      [first.pairs[0].id]: { from: "duplicateFrom" },
      [second.pairs[0].id]: { from: "duplicateFrom" },
    });
  });

  it("allows empty type and model as a global mapping", () => {
    const row = createReasoningEffortMappingRow({
      from: "max",
      to: "low",
    });
    expect(row.match_type).toBe("");
    expect(row.model).toBe("");
    expect(validateReasoningEffortMappings([row], "openai")).toEqual({});
  });

  it("treats prefix without a model as a global mapping", () => {
    const row = createReasoningEffortMappingRow({
      from: "max",
      to: "low",
      match_type: "prefix",
    });
    expect(validateReasoningEffortMappings([row], "openai")).toEqual({});
    expect(reasoningEffortMappingsToAPI([row])).toEqual([
      { from: "max", to: "low" },
    ]);
  });

  it("requires a model scope for unrankable custom mappings", () => {
    const row = createReasoningEffortMappingRow({ from: "ultra", to: "high" });
    expect(validateReasoningEffortMappings([row], "openai")).toEqual({
      [row.pairs[0].id]: { from: "sourceModelRequired" },
    });
  });

  it('keeps disabled distinct from none and numeric sources distinct from strings', () => {
    const rows = reasoningEffortMappingsToRows([
      { from: 'disabled', to: 'deny', model: 'gpt-example' },
      { from: 'none', to: 'low', model: 'gpt-example' },
      { from: '42', to: 'medium', model: 'gpt-example' },
      { from: '00042', from_type: 'number', to: 'high', model: 'gpt-example' },
      { from: 'Custom', to: 'low', model: 'gpt-example' },
      { from: 'custom', to: 'high', model: 'gpt-example' },
    ]);
    expect(validateReasoningEffortMappings(rows)).toEqual({});
    expect(reasoningEffortMappingsToAPI(rows).map(({ from, from_type }) => [from, from_type])).toEqual([
      ['disabled', undefined], ['none', undefined], ['42', undefined], ['42', 'number'], ['Custom', undefined], ['custom', undefined],
    ]);
  });

  it.each(['string', 'number'] as const)('rejects custom %s mappings for Anthropic even with a model scope', (from_type) => {
    const row = createReasoningEffortMappingRow({ from: from_type === 'number' ? '42' : 'disabled', from_type, to: 'high', model: 'claude-example' });
    expect(validateReasoningEffortMappings([row], 'anthropic')[row.pairs[0].id]).toEqual({ from: 'unsupportedFrom' });
  });

  it.each(['-1', '+1', '1.5', '1e2', '18446744073709551616', '100000000000000000000'])('rejects invalid numeric source %s', (from) => {
    const row = createReasoningEffortMappingRow({ from, from_type: 'number', to: 'high', model: 'gpt-example' });
    expect(validateReasoningEffortMappings([row])[row.pairs[0].id]).toEqual({ from: 'invalidNumericSource' });
  });

  it('rejects custom values over the source limit without discarding persisted values', () => {
    const from = 'x'.repeat(65);
    const rows = reasoningEffortMappingsToRows([{ from, to: 'high', model: 'gpt-example' }]);
    expect(rows[0].pairs[0].from).toBe(from);
    expect(validateReasoningEffortMappings(rows)[rows[0].pairs[0].id]).toEqual({ from: 'sourceTooLong' });
  });

  it('detects duplicate numeric values after canonicalization', () => {
    const row = createReasoningEffortMappingRow({ from: '42', from_type: 'number', to: 'high', model: 'gpt-example' });
    row.pairs.push(createReasoningEffortMappingPair({ from: '0042', from_type: 'number', to: 'low' }));
    expect(validateReasoningEffortMappings([row])).toEqual(Object.fromEntries(row.pairs.map(({ id }) => [id, { from: 'duplicateFrom' }])));
  });

  it("normalizes over-limit access control to downgrade or deny", () => {
    expect(normalizeReasoningEffortOverLimit(undefined)).toBe(
      reasoningEffortOverLimitDowngrade,
    );
    expect(normalizeReasoningEffortOverLimit("")).toBe(
      reasoningEffortOverLimitDowngrade,
    );
    expect(normalizeReasoningEffortOverLimit(" downgrade ")).toBe(
      reasoningEffortOverLimitDowngrade,
    );
    expect(normalizeReasoningEffortOverLimit("DENY")).toBe(
      reasoningEffortOverLimitDeny,
    );
    expect(normalizeReasoningEffortOverLimit("block")).toBe(
      reasoningEffortOverLimitDowngrade,
    );
  });
});
