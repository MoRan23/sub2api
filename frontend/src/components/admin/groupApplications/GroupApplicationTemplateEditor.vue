<template>
  <section class="space-y-5 border-t border-gray-200 pt-6 dark:border-dark-700">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
      <div>
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
          {{ t("admin.groupApplications.mailTemplates") }}
        </h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
          {{ t("admin.groupApplications.templateHint") }}
        </p>
      </div>
      <div class="w-full sm:w-72">
        <label class="input-label mb-1.5 block">
          {{ t("admin.groupApplications.templateType") }}
        </label>
        <Select
		  v-model="kind"
		  :options="kindOptions"
		  :searchable="false"
		  :aria-label="t('admin.groupApplications.templateType')"
		/>
      </div>
    </div>

    <div class="border-b border-gray-200 dark:border-dark-700">
      <div class="flex gap-5" role="tablist">
        <button
          v-for="item in locales"
          :key="item.value"
          type="button"
          role="tab"
          :aria-selected="locale === item.value"
          class="border-b-2 px-1 py-2 text-sm font-medium transition-colors"
          :class="
            locale === item.value
              ? 'border-primary-500 text-primary-600 dark:text-primary-400'
              : 'border-transparent text-gray-500 hover:text-gray-800 dark:text-dark-400 dark:hover:text-dark-100'
          "
          @click="locale = item.value"
        >
          {{ item.label }}
        </button>
      </div>
    </div>

    <div class="space-y-4">
      <Input
        v-model="model[kind][locale].subject"
        :label="t('admin.groupApplications.subject')"
        :placeholder="t('admin.groupApplications.subjectPlaceholder')"
		:maxlength="300"
      />
      <label class="block">
        <span class="input-label mb-1.5 block">HTML</span>
        <textarea
          v-model="model[kind][locale].html"
          class="input min-h-56 w-full resize-y font-mono text-xs leading-5"
          maxlength="100000"
          spellcheck="false"
        />
      </label>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from "vue";
import { useI18n } from "vue-i18n";
import Input from "@/components/common/Input.vue";
import Select from "@/components/common/Select.vue";
import type {
  GroupApplicationMailKind,
  GroupApplicationTemplateSet,
} from "@/api/admin/groupApplications";

const model = defineModel<GroupApplicationTemplateSet>({ required: true });
const { t } = useI18n();
const locale = ref<"zh" | "en">("zh");
const kind = ref<GroupApplicationMailKind>("approval");
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
const kindOptions = computed(() =>
  kinds.map((value) => ({
    value,
    label: t(`admin.groupApplications.templateKinds.${value}`),
  })),
);
</script>
