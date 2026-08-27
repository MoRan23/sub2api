<template>
  <div class="space-y-5">
    <div
      class="flex items-center justify-between border-b border-gray-200 pb-3 dark:border-dark-600"
    >
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t("admin.groupApplications.mailTemplates") }}
      </h3>
      <div
        class="inline-flex overflow-hidden rounded border border-gray-300 dark:border-dark-600"
      >
        <button
          v-for="item in locales"
          :key="item.value"
          type="button"
          class="px-3 py-1.5 text-xs font-medium"
          :class="
            locale === item.value
              ? 'bg-primary-600 text-white'
              : 'bg-white text-gray-600 dark:bg-dark-800 dark:text-dark-300'
          "
          @click="locale = item.value"
        >
          {{ item.label }}
        </button>
      </div>
    </div>
    <section
      v-for="kind in kinds"
      :key="kind"
      class="border-b border-gray-200 pb-5 last:border-0 dark:border-dark-600"
    >
      <h4 class="mb-3 text-sm font-medium text-gray-800 dark:text-dark-100">
        {{ t(`admin.groupApplications.templateKinds.${kind}`) }}
      </h4>
      <label class="form-group">
        <span class="form-label">{{
          t("admin.groupApplications.subject")
        }}</span>
        <input
          v-model="model[kind][locale].subject"
          class="form-input"
          maxlength="300"
        />
      </label>
      <label class="form-group mt-3">
        <span class="form-label">HTML</span>
        <textarea
          v-model="model[kind][locale].html"
          class="form-input min-h-32 font-mono text-xs"
          maxlength="100000"
        />
      </label>
    </section>
  </div>
</template>

<script setup lang="ts">
import { ref } from "vue";
import { useI18n } from "vue-i18n";
import type {
  GroupApplicationMailKind,
  GroupApplicationTemplateSet,
} from "@/api/admin/groupApplications";

const model = defineModel<GroupApplicationTemplateSet>({ required: true });
const { t } = useI18n();
const locale = ref<"zh" | "en">("zh");
const locales: Array<{ value: "zh" | "en"; label: string }> = [
  { value: "zh", label: "中文" },
  { value: "en", label: "English" },
];
const kinds: GroupApplicationMailKind[] = [
  "approval",
  "completion",
  "manual_rejection",
  "reply_mismatch",
  "revocation",
];
</script>
