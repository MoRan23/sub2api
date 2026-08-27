<template>
  <BaseDialog
    :show="show"
    :title="t('groupApplications.title')"
    width="wide"
    @close="emit('close')"
  >
    <div class="space-y-6">
      <section v-if="selectableOptions.length" class="space-y-4">
        <div>
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t("groupApplications.newApplication") }}
          </h4>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
            {{ t("groupApplications.emailNotice") }}
          </p>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label class="input-label mb-1.5 block">{{ t("groupApplications.group") }}</label>
            <Select
              v-model="form.group_id"
              :options="selectableGroupOptions"
              :placeholder="t('groupApplications.selectGroup')"
			  :aria-label="t('groupApplications.group')"
              searchable
            />
          </div>
          <Input
            v-model="form.contact_email"
            type="email"
            :label="t('groupApplications.contactEmail')"
            autocomplete="email"
          />
        </div>
        <div>
          <TextArea
            v-model="form.reason"
            :label="t('groupApplications.reason')"
            :rows="5"
			:maxlength="5000"
          />
          <div class="mt-1 text-right text-xs text-gray-400">{{ form.reason.length }}/5000</div>
        </div>
        <div class="flex justify-end">
          <button
            class="btn btn-primary"
            :disabled="submitting || !canSubmit"
            @click="submit"
          >
            <Icon name="mail" size="sm" class="mr-2" />
            {{
              submitting
                ? t("common.submitting")
                : t("groupApplications.submit")
            }}
          </button>
        </div>
      </section>

      <section>
        <div class="mb-3 flex items-center justify-between">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t("groupApplications.history") }}
          </h4>
          <button
            class="btn btn-secondary px-2"
            :title="t('common.refresh')"
            :disabled="loading"
            @click="load"
          >
            <Icon
              name="refresh"
              size="sm"
              :class="loading ? 'animate-spin' : ''"
            />
          </button>
        </div>
        <div v-if="loading" class="py-8 text-center text-sm text-gray-500">
          {{ t("common.loading") }}
        </div>
        <div
          v-else-if="!applications.length"
          class="rounded border border-dashed border-gray-300 px-4 py-8 text-center text-sm text-gray-500 dark:border-dark-600"
        >
          {{ t("groupApplications.noHistory") }}
        </div>
        <div
          v-else
          class="divide-y divide-gray-200 border-y border-gray-200 dark:divide-dark-600 dark:border-dark-600"
        >
          <article v-for="item in applications" :key="item.id" class="py-4">
            <div class="flex flex-wrap items-start justify-between gap-2">
              <div>
                <div class="font-medium text-gray-900 dark:text-white">
                  {{ item.group_name }}
                </div>
                <div class="mt-1 text-xs text-gray-500">
                  {{ formatDate(item.created_at) }} · {{ item.contact_email }}
                </div>
              </div>
              <span
                :class="statusClass(item.status)"
                class="rounded px-2 py-1 text-xs font-medium"
              >
                {{ t(`groupApplications.status.${item.status}`) }}
              </span>
            </div>
            <p
              class="mt-3 whitespace-pre-wrap text-sm text-gray-600 dark:text-dark-300"
            >
              {{ item.reason }}
            </p>
            <p
              v-if="item.status === 'awaiting_reply'"
              class="mt-3 text-sm text-amber-700 dark:text-amber-300"
            >
              {{ t("groupApplications.awaitingReplyHint") }}
            </p>
            <p
              v-if="item.decision_reason"
              class="mt-3 text-sm text-red-600 dark:text-red-300"
            >
              {{ t("groupApplications.decisionReason") }}:
              {{ item.decision_reason }}
            </p>
            <p
              v-if="item.last_email_status === 'failed'"
              class="mt-2 text-xs text-red-600"
            >
              {{ t("groupApplications.emailFailed") }}:
              {{ item.last_email_error }}
            </p>
          </article>
        </div>
      </section>
    </div>
    <template #footer>
      <button class="btn btn-secondary" @click="emit('close')">
        {{ t("common.close") }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from "vue";
import { useI18n } from "vue-i18n";
import {
  groupApplicationsAPI,
  type GroupApplication,
  type GroupApplicationOption,
  type GroupApplicationStatus,
} from "@/api";
import { useAppStore } from "@/stores/app";
import BaseDialog from "@/components/common/BaseDialog.vue";
import Input from "@/components/common/Input.vue";
import Select from "@/components/common/Select.vue";
import TextArea from "@/components/common/TextArea.vue";
import Icon from "@/components/icons/Icon.vue";

const props = defineProps<{ show: boolean }>();
const emit = defineEmits<{ close: []; changed: [] }>();
const { t, locale } = useI18n();
const appStore = useAppStore();
const loading = ref(false);
const submitting = ref(false);
const options = ref<GroupApplicationOption[]>([]);
const applications = ref<GroupApplication[]>([]);
const form = reactive({ group_id: 0, contact_email: "", reason: "" });

const selectableOptions = computed(() =>
  options.value.filter((item) => !item.has_active && !item.already_completed),
);
const selectableGroupOptions = computed(() =>
  selectableOptions.value.map((item) => ({
    value: item.group_id,
    label: item.group_name,
  })),
);
const canSubmit = computed(
  () =>
    form.group_id > 0 &&
    /^\S+@\S+\.\S+$/.test(form.contact_email) &&
    form.reason.trim().length >= 5,
);

async function load() {
  loading.value = true;
  try {
    const [available, history] = await Promise.all([
      groupApplicationsAPI.options(),
      groupApplicationsAPI.list(),
    ]);
    options.value = available;
    applications.value = history;
    if (
      !selectableOptions.value.some((item) => item.group_id === form.group_id)
    )
      form.group_id = selectableOptions.value[0]?.group_id ?? 0;
  } catch (error) {
    appStore.showError(
      error instanceof Error ? error.message : t("common.error"),
    );
  } finally {
    loading.value = false;
  }
}

async function submit() {
  if (!canSubmit.value) return;
  submitting.value = true;
  try {
    await groupApplicationsAPI.create({ ...form, locale: locale.value });
    form.reason = "";
    appStore.showSuccess(t("groupApplications.submitted"));
    await load();
    emit("changed");
  } catch (error) {
    appStore.showError(
      error instanceof Error ? error.message : t("common.error"),
    );
  } finally {
    submitting.value = false;
  }
}

function formatDate(value: string) {
  return new Date(value).toLocaleString();
}
function statusClass(status: GroupApplicationStatus) {
  return {
    pending: "bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300",
    awaiting_reply:
      "bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300",
    completed:
      "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300",
    rejected: "bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300",
    revoked: "bg-gray-200 text-gray-700 dark:bg-dark-600 dark:text-dark-200",
  }[status];
}

watch(
  () => props.show,
  (visible) => {
    if (visible) void load();
  },
  { immediate: true },
);
</script>
