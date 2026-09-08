import { defineComponent, h, type PropType } from "vue";
import { cleanup, fireEvent, render, waitFor } from "@testing-library/vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminGroup, CodexModelsManifestConfig } from "@/types";
import GroupsView from "@/views/admin/GroupsView.vue";

const {
  listGroups,
  getModelAllowlistCandidates,
  updateGroup,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  updateGroup: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
}));

const authState = vi.hoisted(() => ({ isSimpleMode: false }));

vi.mock("@/api/admin", () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: vi.fn(),
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      create: vi.fn(),
      update: updateGroup,
      delete: vi.fn(),
      duplicate: vi.fn(),
      updateSortOrder: vi.fn(),
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn(),
    },
  },
}));

vi.mock("@/stores/auth", () => ({
  useAuthStore: () => authState,
}));

vi.mock("@/stores/app", () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}));

vi.mock("@/stores/onboarding", () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn(),
  }),
}));

vi.mock("vue-i18n", async () => {
  const actual = await vi.importActual<typeof import("vue-i18n")>("vue-i18n");
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  };
});

const sourceGroup = {
  id: 42,
  name: "OpenAI",
  description: null,
  platform: "openai",
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: "active",
  subscription_type: "standard",
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  total_limit_usd: null,
  long_context_pricing_enabled: true,
  force_openai_fast: false,
  free_openai_fast: false,
  model_pricing: [],
  profit_control_enabled: false,
  profit_min_margin: 0,
  profit_safety_buffer: 0,
  allow_image_generation: false,
  allow_batch_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null,
  video_price_720p: null,
  video_price_1080p: null,
  web_search_price_per_call: null,
  search_price_per_1k: null,
  audio_realtime_price_per_min: null,
  audio_tts_price_per_million_chars: null,
  audio_stt_price_per_hour: null,
  peak_rate_enabled: false,
  peak_start: "",
  peak_end: "",
  peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  allow_live: false,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 1,
  active_account_count: 1,
  rate_limited_account_count: 0,
  model_allowlist: undefined,
  codex_models_manifest_config: {
    enabled: false,
    account_ids: [],
    fallback_to_scheduler: false,
  },
  sort_order: 10,
} satisfies AdminGroup;

const AppLayoutStub = defineComponent({
  template: "<main><slot /></main>",
});

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
});

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
  },
  template: '<div><div v-if="data.length"><slot name="cell-actions" :row="data[0]" /></div></div>',
});

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
});

const CodexManifestAccountsFieldStub = defineComponent({
  name: "CodexManifestAccountsField",
  props: {
    modelValue: {
      type: Object as PropType<CodexModelsManifestConfig>,
      required: true,
    },
  },
  emits: ["update:modelValue"],
  setup(props, { emit, expose }) {
    expose({ validate: () => true, resetValidation: () => undefined });
    return () =>
      h("div", { "data-testid": "codex-manifest-field" }, [
        h(
          "output",
          { "data-testid": "codex-manifest-value" },
          JSON.stringify(props.modelValue),
        ),
        h(
          "button",
          {
            type: "button",
            "data-testid": "codex-manifest-enable",
            onClick: () =>
              emit("update:modelValue", {
                ...props.modelValue,
                enabled: true,
              }),
          },
          "enable",
        ),
        h(
          "button",
          {
            type: "button",
            "data-testid": "codex-manifest-select-account",
            onClick: () =>
              emit("update:modelValue", {
                ...props.modelValue,
                account_ids: [17],
              }),
          },
          "select account",
        ),
      ]);
  },
});

const mountView = () =>
  render(GroupsView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        CodexManifestAccountsField: CodexManifestAccountsFieldStub,
        PricingEntryCard: true,
        VueDraggable: true,
      },
    },
  });

describe("GroupsView Codex manifest binding", () => {
  afterEach(cleanup);

  beforeEach(() => {
    authState.isSimpleMode = false;
    localStorage.clear();
    listGroups.mockReset();
    getModelAllowlistCandidates.mockReset();
    updateGroup.mockReset();
    getUsageSummary.mockReset();
    getCapacitySummary.mockReset();
    getLiveCapability.mockReset();

    listGroups.mockResolvedValue({
      items: [sourceGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    });
    getModelAllowlistCandidates.mockResolvedValue([]);
    updateGroup.mockResolvedValue(sourceGroup);
    getUsageSummary.mockResolvedValue([]);
    getCapacitySummary.mockResolvedValue([]);
    getLiveCapability.mockResolvedValue({ supported: false });
  });

  it("preserves consecutive child updates on the reactive edit config", async () => {
    const view = mountView();
    await fireEvent.click(await view.findByRole("button", { name: "common.edit" }));

    expect(view.getByTestId("codex-manifest-value").textContent).toBe(
      JSON.stringify(sourceGroup.codex_models_manifest_config),
    );

    await fireEvent.click(view.getByRole("button", { name: "enable" }));
    expect(view.getByTestId("codex-manifest-value").textContent).toContain(
      '"enabled":true',
    );

    await fireEvent.click(view.getByRole("button", { name: "select account" }));
    expect(view.getByTestId("codex-manifest-value").textContent).toBe(
      JSON.stringify({
        enabled: true,
        account_ids: [17],
        fallback_to_scheduler: false,
      }),
    );
  });

  it.each([false, true])("keeps total quota isolated from simple mode (%s)", async (simpleMode) => {
    authState.isSimpleMode = simpleMode;
    listGroups.mockResolvedValueOnce({
      items: [{ ...sourceGroup, subscription_type: "total_quota", total_limit_usd: 25 }],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    });
    const view = mountView();
    await fireEvent.click(await view.findByRole("button", { name: "common.edit" }));

    const totalInput = view.queryByPlaceholderText("admin.groups.subscription.totalLimitPlaceholder");
    if (simpleMode) {
      expect(totalInput).toBeNull();
      expect(view.queryByTestId("codex-manifest-field")).toBeNull();
    } else {
      expect(totalInput).not.toBeNull();
      expect((totalInput as HTMLInputElement).value).toBe("25");
      await fireEvent.update(totalInput!, "30");
    }

    await fireEvent.submit(view.container.querySelector("#edit-group-form")!);
    await waitFor(() => expect(updateGroup).toHaveBeenCalledTimes(1));
    if (simpleMode) {
      expect(updateGroup).toHaveBeenCalledWith(42, { name: "OpenAI", description: "" });
    } else {
      expect(updateGroup).toHaveBeenCalledWith(42, expect.objectContaining({
        subscription_type: "total_quota",
        total_limit_usd: 30,
      }));
    }
  });
});
