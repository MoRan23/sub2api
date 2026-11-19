import { mount } from "@vue/test-utils";
import { describe, expect, it, vi } from "vitest";

import ReasoningEffortPolicyFields from "../ReasoningEffortPolicyFields.vue";
import { createReasoningEffortMappingRow } from "@/views/admin/groupsReasoningEffort";

vi.mock("vue-i18n", async () => {
  const actual = await vi.importActual<typeof import("vue-i18n")>("vue-i18n");
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  };
});

describe("ReasoningEffortPolicyFields", () => {
  it('does not offer custom or numeric source modes for Anthropic', () => {
    const mapping = createReasoningEffortMappingRow({ from: 'high', to: 'low' });
    const wrapper = mount(ReasoningEffortPolicyFields, {
      props: { idPrefix: 'anthropic', platform: 'anthropic', maxEffort: '', overLimit: 'downgrade', mappings: [mapping] },
      global: { stubs: { Icon: true, Select: { props: ['id', 'options'], template: '<div :id="id"><span v-for="option in options" :key="option.value" :data-value="option.value" /></div>' } } },
    });
    expect(wrapper.find(`#anthropic-${mapping.pairs[0].id}-source-kind`).exists()).toBe(false);
  });

  it('edits numeric sources without converting them to JavaScript numbers', async () => {
    const mapping = createReasoningEffortMappingRow({ from: '18446744073709551615', from_type: 'number', to: 'high', model: 'gpt-example' });
    const wrapper = mount(ReasoningEffortPolicyFields, {
      props: { idPrefix: 'numeric', platform: 'openai', maxEffort: '', overLimit: 'downgrade', mappings: [mapping] },
      global: { stubs: { Icon: true, Select: { props: ['id', 'modelValue', 'options'], emits: ['update:model-value'], template: '<select :id="id" :value="modelValue" @change="$emit(\'update:model-value\', $event.target.value)"><option value="preset">preset</option><option value="custom">custom</option><option value="number">number</option></select>' } } },
    });
    const input = wrapper.get(`#numeric-${mapping.pairs[0].id}-from`);
    expect(input.attributes('type')).toBe('text');
    expect(input.attributes('inputmode')).toBe('numeric');
    await input.setValue('18446744073709551614');
    expect(wrapper.emitted('update:mappings')?.[0]?.[0]).toMatchObject([{ pairs: [{ from: '18446744073709551614', from_type: 'number' }] }]);
    await wrapper.get(`#numeric-${mapping.pairs[0].id}-source-kind`).setValue('custom');
    expect(wrapper.emitted('update:mappings')?.[1]?.[0]).toMatchObject([{ pairs: [{ from: '', from_type: 'string', source_kind: 'custom' }] }]);
  });

  it('keeps a model-specific custom value available for editing', () => {
    const mapping = createReasoningEffortMappingRow({ from: 'disabled', to: 'deny', model: 'gpt-example' });
    const wrapper = mount(ReasoningEffortPolicyFields, {
      props: { idPrefix: 'custom', platform: 'openai', maxEffort: 'high', overLimit: 'deny', mappings: [mapping] },
      global: { stubs: { Icon: true, Select: true } },
    });
    expect(wrapper.get(`#custom-${mapping.pairs[0].id}-from`).element).toMatchObject({ value: 'disabled' });
    expect(wrapper.text()).toContain('reasoningEffortCustomHint');
  });

  it("offers none only in the mapping source selector", () => {
    const mapping = createReasoningEffortMappingRow({
      from: "none",
      to: "low",
    });
    const wrapper = mount(ReasoningEffortPolicyFields, {
      props: {
        idPrefix: "edit-group-reasoning",
        platform: "openai",
        maxEffort: "",
        overLimit: "downgrade",
        mappings: [mapping],
      },
      global: {
        stubs: {
          Icon: true,
          Select: {
            props: ["id", "options"],
            template:
              '<div :id="id"><span v-for="option in options" :key="option.value" :data-value="option.value" /></div>',
          },
        },
      },
    });

    const optionValues = (id: string) =>
      wrapper
        .get(`#${id}`)
        .findAll("[data-value]")
        .map((option) => option.attributes("data-value"));

    expect(optionValues("edit-group-reasoning-max-effort")).not.toContain(
      "none",
    );
    expect(optionValues("edit-group-reasoning-max-effort")).not.toContain(
      "deny",
    );
    expect(
      optionValues(`edit-group-reasoning-${mapping.pairs[0].id}-from`),
    ).toContain("none");
    expect(
      optionValues(`edit-group-reasoning-${mapping.pairs[0].id}-from`),
    ).not.toContain("deny");
    expect(
      optionValues(`edit-group-reasoning-${mapping.pairs[0].id}-to`),
    ).not.toContain("none");
    expect(
      optionValues(`edit-group-reasoning-${mapping.pairs[0].id}-to`),
    ).toContain("deny");
  });

  it("renders model scope fields for each mapping", () => {
    const mapping = createReasoningEffortMappingRow({
      from: "max",
      to: "low",
      match_type: "prefix",
      model: "gpt",
    });
    const wrapper = mount(ReasoningEffortPolicyFields, {
      props: {
        idPrefix: "create-group-reasoning",
        platform: "openai",
        maxEffort: "",
        overLimit: "downgrade",
        mappings: [mapping],
      },
      global: {
        stubs: {
          Icon: { props: ["name"], template: '<i :data-icon="name" />' },
          Select: {
            props: ["id", "modelValue", "placeholder"],
            template:
              '<input :id="id" :value="modelValue ?? \'\'" :placeholder="placeholder" />',
          },
        },
      },
    });

    expect(wrapper.text()).toContain("admin.groups.form.reasoningEffortMatchType");
    expect(wrapper.text()).toContain("admin.groups.form.reasoningEffortModel");
    expect(wrapper.text()).toContain("admin.groups.form.reasoningEffortFrom");
    expect(wrapper.text()).toContain("admin.groups.form.reasoningEffortTo");
    expect(wrapper.text()).toContain("admin.groups.form.addReasoningEffortPair");

    const modelInput = wrapper.get(
      `#create-group-reasoning-${mapping.id}-model`,
    );
    expect(modelInput.element).toMatchObject({
      value: "gpt",
      placeholder: "admin.groups.form.reasoningEffortModelPlaceholder",
    });
  });

  it("emits a new mapping group with one empty pair", async () => {
    const wrapper = mount(ReasoningEffortPolicyFields, {
      props: {
        idPrefix: "edit-group-reasoning",
        platform: "openai",
        maxEffort: "",
        overLimit: "downgrade",
        mappings: [],
      },
      global: {
        stubs: {
          Icon: { props: ["name"], template: '<i :data-icon="name" />' },
          Select: { template: "<div />" },
        },
      },
    });

    await wrapper.get("button").trigger("click");

    const emitted = wrapper.emitted("update:mappings");
    expect(emitted).toHaveLength(1);
    expect(emitted?.[0]?.[0]).toEqual([
      expect.objectContaining({
        match_type: "",
        model: "",
        pairs: [
          expect.objectContaining({
            from: "",
            to: "",
          }),
        ],
      }),
    ]);
  });
});
