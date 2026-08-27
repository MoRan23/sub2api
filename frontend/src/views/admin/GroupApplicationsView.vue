<template>
  <AppLayout>
    <div class="mx-auto w-full max-w-7xl px-4 py-6 sm:px-6 lg:px-8">
      <div
        class="mb-6 flex flex-wrap items-center justify-between gap-4 border-b border-gray-200 pb-4 dark:border-dark-600"
      >
        <div
          class="inline-flex overflow-hidden rounded border border-gray-300 dark:border-dark-600"
        >
          <button
            v-for="item in tabs"
            :key="item.value"
            class="px-4 py-2 text-sm font-medium"
            :class="
              activeTab === item.value
                ? 'bg-primary-600 text-white'
                : 'bg-white text-gray-600 dark:bg-dark-800 dark:text-dark-300'
            "
            @click="activeTab = item.value"
          >
            {{ item.label }}
          </button>
        </div>
        <button
          class="btn btn-secondary"
          :disabled="loading"
          @click="reloadActiveTab"
        >
          <Icon
            name="refresh"
            size="sm"
            :class="loading ? 'animate-spin' : ''"
          />
        </button>
      </div>

      <section v-if="activeTab === 'applications'">
        <div class="mb-4 flex flex-wrap gap-3">
          <input
            v-model.trim="filters.search"
            class="form-input w-full sm:w-64"
            :placeholder="t('admin.groupApplications.search')"
            @keyup.enter="applyFilters"
          />
          <select
            v-model="filters.status"
            class="form-input w-48"
            @change="applyFilters"
          >
            <option value="">
              {{ t("admin.groupApplications.allStatuses") }}
            </option>
            <option v-for="status in statuses" :key="status" :value="status">
              {{ t(`groupApplications.status.${status}`) }}
            </option>
          </select>
          <button class="btn btn-primary" @click="applyFilters">
            <Icon name="search" size="sm" class="mr-2" />{{
              t("common.search")
            }}
          </button>
        </div>
        <div
          class="overflow-x-auto border-y border-gray-200 dark:border-dark-600"
        >
          <table
            class="min-w-full divide-y divide-gray-200 dark:divide-dark-600"
          >
            <thead class="bg-gray-50 dark:bg-dark-800">
              <tr>
                <th
                  v-for="heading in applicationHeadings"
                  :key="heading"
                  class="px-4 py-3 text-left text-xs font-semibold uppercase text-gray-500"
                >
                  {{ heading }}
                </th>
              </tr>
            </thead>
            <tbody
              class="divide-y divide-gray-200 bg-white dark:divide-dark-600 dark:bg-dark-900"
            >
              <tr
                v-for="item in applications"
                :key="item.id"
                class="cursor-pointer hover:bg-gray-50 dark:hover:bg-dark-800"
                @click="openApplication(item.id)"
              >
                <td class="px-4 py-3 text-sm">#{{ item.id }}</td>
                <td class="px-4 py-3 text-sm">
                  <div class="font-medium text-gray-900 dark:text-white">
                    {{ item.user_email }}
                  </div>
                  <div class="text-xs text-gray-500">
                    UID {{ item.user_id }}
                  </div>
                </td>
                <td class="px-4 py-3 text-sm">{{ item.group_name }}</td>
                <td class="px-4 py-3 text-sm">{{ item.contact_email }}</td>
                <td class="px-4 py-3">
                  <span
                    :class="statusClass(item.status)"
                    class="rounded px-2 py-1 text-xs font-medium"
                    >{{ t(`groupApplications.status.${item.status}`) }}</span
                  >
                </td>
                <td class="px-4 py-3 text-sm text-gray-500">
                  {{ formatDate(item.created_at) }}
                </td>
                <td class="px-4 py-3">
                  <span
                    v-if="item.last_email_status"
                    class="text-xs"
                    :class="
                      item.last_email_status === 'failed'
                        ? 'text-red-600'
                        : 'text-gray-500'
                    "
                    >{{ item.last_email_status }}</span
                  >
                </td>
              </tr>
              <tr v-if="!loading && !applications.length">
                <td
                  :colspan="7"
                  class="px-4 py-12 text-center text-sm text-gray-500"
                >
                  {{ t("admin.groupApplications.noApplications") }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div
          v-if="totalApplications > pageSize"
          class="mt-4 flex items-center justify-end gap-3 text-sm text-gray-500"
        >
          <button
            class="btn btn-secondary px-2"
            :title="t('common.back')"
            :disabled="page <= 1 || loading"
            @click="changePage(page - 1)"
          >
            <Icon name="chevronLeft" size="sm" />
          </button>
          <span>{{ page }} / {{ totalPages }}</span>
          <button
            class="btn btn-secondary px-2"
            :title="t('common.next')"
            :disabled="page >= totalPages || loading"
            @click="changePage(page + 1)"
          >
            <Icon name="chevronRight" size="sm" />
          </button>
        </div>
      </section>

      <section
        v-else-if="activeTab === 'policies'"
        class="grid gap-6 lg:grid-cols-[280px_minmax(0,1fr)]"
      >
        <aside
          class="border-r border-gray-200 pr-0 lg:pr-6 dark:border-dark-600"
        >
          <label class="form-group">
            <span class="form-label">{{
              t("admin.groupApplications.applyableGroup")
            }}</span>
            <select
              v-model.number="selectedGroupID"
              class="form-input"
              @change="selectPolicy(selectedGroupID)"
            >
              <option :value="0" disabled>
                {{ t("groupApplications.selectGroup") }}
              </option>
              <option
                v-for="group in eligibleGroups"
                :key="group.id"
                :value="group.id"
              >
                {{ group.name }}
              </option>
            </select>
          </label>
          <div class="mt-4 space-y-1">
            <button
              v-for="policy in policies"
              :key="policy.group_id"
              class="flex w-full items-center justify-between px-3 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-dark-800"
              :class="
                selectedGroupID === policy.group_id
                  ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300'
                  : ''
              "
              @click="selectPolicy(policy.group_id)"
            >
              <span>{{ policy.group_name }}</span
              ><span
                class="h-2 w-2 rounded-full"
                :class="policy.enabled ? 'bg-green-500' : 'bg-gray-300'"
              />
            </button>
          </div>
        </aside>
        <div v-if="policyForm" class="space-y-6">
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="flex items-center gap-3"
              ><input
                v-model="policyForm.enabled"
                type="checkbox"
                class="h-4 w-4"
              /><span class="text-sm font-medium">{{
                t("admin.groupApplications.enabled")
              }}</span></label
            >
            <label class="form-group"
              ><span class="form-label">{{
                t("admin.groupApplications.replyPhrase")
              }}</span
              ><input
                v-model.trim="policyForm.reply_phrase"
                class="form-input"
                maxlength="500"
            /></label>
          </div>
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="form-group"
              ><span class="form-label">{{
                t("admin.groupApplications.pdfAgreement")
              }}</span
              ><input
                ref="fileInput"
                type="file"
                accept="application/pdf,.pdf"
                class="form-input"
                @change="handleAttachment"
            /></label>
            <div
              v-if="policyForm.attachment_name"
              class="flex items-center justify-between gap-3 self-end text-sm text-gray-500"
            >
              <span>
                {{ policyForm.attachment_name }} ·
                {{ formatBytes(policyForm.attachment_size || 0) }}
              </span>
              <button
                v-if="policyForm.attachment_id"
                class="btn btn-secondary px-2"
                :title="t('admin.groupApplications.downloadAgreement')"
                @click="downloadAgreement"
              >
                <Icon name="download" size="sm" />
              </button>
            </div>
          </div>
          <GroupApplicationTemplateEditor v-model="policyForm.templates" />
          <div
            class="flex justify-end border-t border-gray-200 pt-4 dark:border-dark-600"
          >
            <button
              class="btn btn-primary"
              :disabled="saving"
              @click="savePolicy"
            >
              {{ t("common.save") }}
            </button>
          </div>
        </div>
      </section>

      <section v-else-if="imapForm" class="max-w-3xl space-y-6">
        <div
          class="flex items-center justify-between border-b border-gray-200 pb-4 dark:border-dark-600"
        >
          <label class="flex items-center gap-3"
            ><input
              v-model="imapForm.enabled"
              type="checkbox"
              class="h-4 w-4"
            /><span class="font-medium">{{
              t("admin.groupApplications.enableIMAP")
            }}</span></label
          >
          <span
            class="text-xs"
            :class="workerHealth?.running ? 'text-green-600' : 'text-red-600'"
            >{{
              workerHealth?.running
                ? t("admin.groupApplications.workerRunning")
                : t("admin.groupApplications.workerStopped")
            }}</span
          >
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="form-group"
            ><span class="form-label">Host</span
            ><input v-model.trim="imapForm.host" class="form-input"
          /></label>
          <label class="form-group"
            ><span class="form-label">Port</span
            ><input
              v-model.number="imapForm.port"
              type="number"
              min="1"
              max="65535"
              class="form-input"
          /></label>
          <label class="form-group"
            ><span class="form-label">{{
              t("admin.groupApplications.username")
            }}</span
            ><input
              v-model.trim="imapForm.username"
              class="form-input"
              autocomplete="username"
          /></label>
          <label class="form-group"
            ><span class="form-label">{{
              t("admin.groupApplications.password")
            }}</span
            ><input
              v-model="imapForm.password"
              type="password"
              class="form-input"
              autocomplete="new-password"
              :placeholder="imapForm.password_configured ? '********' : ''"
          /></label>
          <label class="form-group"
            ><span class="form-label">{{
              t("admin.groupApplications.mailbox")
            }}</span
            ><input v-model.trim="imapForm.mailbox" class="form-input"
          /></label>
          <label class="form-group"
            ><span class="form-label">{{
              t("admin.groupApplications.replyAddress")
            }}</span
            ><input
              v-model.trim="imapForm.reply_address"
              type="email"
              class="form-input"
          /></label>
          <label class="form-group"
            ><span class="form-label">TLS</span
            ><select v-model="imapForm.tls_mode" class="form-input">
              <option value="implicit">Implicit TLS</option>
              <option value="starttls">STARTTLS</option>
            </select></label
          >
          <label class="form-group"
            ><span class="form-label">{{
              t("admin.groupApplications.pollInterval")
            }}</span
            ><input
              v-model.number="imapForm.poll_interval_seconds"
              type="number"
              min="15"
              max="300"
              class="form-input"
          /></label>
        </div>
        <p v-if="workerHealth?.last_imap_error" class="text-sm text-red-600">
          {{ workerHealth.last_imap_error }}
        </p>
        <div
          class="flex justify-end gap-3 border-t border-gray-200 pt-4 dark:border-dark-600"
        >
          <button
            class="btn btn-secondary"
            :disabled="testing"
            @click="testIMAP"
          >
            {{ t("admin.groupApplications.testConnection") }}</button
          ><button class="btn btn-primary" :disabled="saving" @click="saveIMAP">
            {{ t("common.save") }}
          </button>
        </div>
      </section>
    </div>

    <BaseDialog
      :show="Boolean(selectedApplication)"
      :title="
        selectedApplication
          ? `#${selectedApplication.id} ${selectedApplication.group_name}`
          : ''
      "
      width="wide"
      @close="closeApplication"
    >
      <div v-if="selectedApplication" class="space-y-5">
        <dl class="grid gap-4 text-sm sm:grid-cols-2">
          <div>
            <dt class="text-gray-500">
              {{ t("admin.groupApplications.user") }}
            </dt>
            <dd>
              {{ selectedApplication.user_email }} ({{
                selectedApplication.user_id
              }})
            </dd>
          </div>
          <div>
            <dt class="text-gray-500">
              {{ t("groupApplications.contactEmail") }}
            </dt>
            <dd>{{ selectedApplication.contact_email }}</dd>
          </div>
        </dl>
        <div>
          <div class="text-sm text-gray-500">
            {{ t("groupApplications.reason") }}
          </div>
          <p class="mt-1 whitespace-pre-wrap text-sm">
            {{ selectedApplication.reason }}
          </p>
        </div>
        <div
          v-if="selectedApplication.decision_reason"
          class="text-sm text-red-600"
        >
          {{ selectedApplication.decision_reason }}
        </div>
        <GroupApplicationCommunicationTimeline
          :communications="communications"
          :loading="communicationsLoading"
          @export="exportCommunications"
          @retry="retryMail"
        />
      </div>
      <template #footer
        ><div class="flex w-full flex-wrap justify-end gap-2">
          <button
            v-if="selectedApplication?.status === 'pending'"
            class="btn btn-primary"
            @click="approveSelected"
          >
            {{ t("admin.groupApplications.approve") }}</button
          ><button
            v-if="
              selectedApplication?.status === 'pending' ||
              selectedApplication?.status === 'awaiting_reply'
            "
            class="btn btn-danger"
            @click="openDecision('reject')"
          >
            {{ t("admin.groupApplications.reject") }}</button
          ><button
            v-if="selectedApplication?.status === 'awaiting_reply'"
            class="btn btn-secondary"
            @click="resendApproval"
          >
            {{ t("admin.groupApplications.resendApproval") }}</button
          ><button
            v-if="selectedApplication?.status === 'completed'"
            class="btn btn-danger"
            @click="openDecision('revoke')"
          >
            {{ t("admin.groupApplications.revoke") }}</button
          ><button
            class="btn btn-secondary"
            @click="closeApplication"
          >
            {{ t("common.close") }}
          </button>
        </div></template
      >
    </BaseDialog>

    <BaseDialog
      :show="decisionMode !== null"
      :title="
        decisionMode === 'reject'
          ? t('admin.groupApplications.reject')
          : t('admin.groupApplications.revoke')
      "
      width="narrow"
      @close="decisionMode = null"
    >
      <label class="form-group"
        ><span class="form-label">{{
          t("admin.groupApplications.decisionReason")
        }}</span
        ><textarea
          v-model.trim="decisionReason"
          class="form-input min-h-28"
          maxlength="2000"
        />
      </label>
      <template #footer
        ><button class="btn btn-secondary" @click="decisionMode = null">
          {{ t("common.cancel") }}</button
        ><button
          class="btn btn-danger"
          :disabled="!decisionReason || saving"
          @click="submitDecision"
        >
          {{ t("common.confirm") }}
        </button></template
      >
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import AppLayout from "@/components/layout/AppLayout.vue";
import BaseDialog from "@/components/common/BaseDialog.vue";
import Icon from "@/components/icons/Icon.vue";
import GroupApplicationTemplateEditor from "@/components/admin/groupApplications/GroupApplicationTemplateEditor.vue";
import GroupApplicationCommunicationTimeline from "@/components/admin/groupApplications/GroupApplicationCommunicationTimeline.vue";
import { adminAPI } from "@/api";
import { useAppStore } from "@/stores/app";
import type { AdminGroup } from "@/types";
import {
  groupApplicationsAdminAPI,
  defaultGroupApplicationTemplates,
  type AdminGroupApplication,
  type GroupApplicationCommunication,
  type GroupApplicationIMAPConfig,
  type GroupApplicationPolicy,
  type GroupApplicationWorkerHealth,
} from "@/api/admin/groupApplications";
import type { GroupApplicationStatus } from "@/api/groupApplications";

const { t } = useI18n();
const appStore = useAppStore();
type Tab = "applications" | "policies" | "imap";
const activeTab = ref<Tab>("applications");
const loading = ref(false);
const saving = ref(false);
const testing = ref(false);
const tabs = computed(() => [
  {
    value: "applications" as const,
    label: t("admin.groupApplications.applications"),
  },
  { value: "policies" as const, label: t("admin.groupApplications.policies") },
  { value: "imap" as const, label: t("admin.groupApplications.imapSettings") },
]);
const statuses: GroupApplicationStatus[] = [
  "pending",
  "awaiting_reply",
  "completed",
  "rejected",
  "revoked",
];
const filters = ref({ status: "" as GroupApplicationStatus | "", search: "" });
const applications = ref<AdminGroupApplication[]>([]);
const totalApplications = ref(0);
const page = ref(1);
const pageSize = 50;
const totalPages = computed(() =>
  Math.max(1, Math.ceil(totalApplications.value / pageSize)),
);
const selectedApplication = ref<AdminGroupApplication | null>(null);
const communications = ref<GroupApplicationCommunication[]>([]);
const communicationsLoading = ref(false);
let applicationRequestVersion = 0;
const policies = ref<GroupApplicationPolicy[]>([]);
const groups = ref<AdminGroup[]>([]);
const selectedGroupID = ref(0);
const policyForm = ref<GroupApplicationPolicy | null>(null);
const attachment = ref<File>();
const imapForm = ref<GroupApplicationIMAPConfig>();
const workerHealth = ref<GroupApplicationWorkerHealth>();
const decisionMode = ref<"reject" | "revoke" | null>(null);
const decisionReason = ref("");
const eligibleGroups = computed(() =>
  groups.value.filter(
    (group) =>
      group.is_exclusive &&
      group.subscription_type === "standard" &&
      group.status === "active",
  ),
);
const applicationHeadings = computed(() => [
  "ID",
  t("admin.groupApplications.user"),
  t("groupApplications.group"),
  t("groupApplications.contactEmail"),
  t("common.status"),
  t("common.createdAt"),
  t("admin.groupApplications.email"),
]);

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : t("common.error");
}
async function loadApplications() {
  loading.value = true;
  try {
    const result = await groupApplicationsAdminAPI.list({
      ...filters.value,
      page: page.value,
      page_size: pageSize,
    });
    applications.value = result.items;
    totalApplications.value = result.total;
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    loading.value = false;
  }
}
async function applyFilters() {
  page.value = 1;
  await loadApplications();
}
async function changePage(nextPage: number) {
  if (nextPage < 1 || nextPage > totalPages.value || nextPage === page.value) return;
  page.value = nextPage;
  await loadApplications();
}
async function openApplication(id: number) {
  const requestVersion = ++applicationRequestVersion;
  communicationsLoading.value = true;
  try {
    const [application, history] = await Promise.all([
      groupApplicationsAdminAPI.get(id),
      groupApplicationsAdminAPI.listCommunications(id),
    ]);
    if (requestVersion === applicationRequestVersion) {
      selectedApplication.value = application;
      communications.value = history;
    }
  } catch (error) {
    if (requestVersion === applicationRequestVersion) {
      appStore.showError(errorMessage(error));
    }
  } finally {
    if (requestVersion === applicationRequestVersion) {
      communicationsLoading.value = false;
    }
  }
}
function closeApplication() {
  applicationRequestVersion += 1;
  selectedApplication.value = null;
  communications.value = [];
  communicationsLoading.value = false;
}
async function loadPolicies() {
  loading.value = true;
  try {
    [policies.value, groups.value] = await Promise.all([
      groupApplicationsAdminAPI.listPolicies(),
      adminAPI.groups.getAll(),
    ]);
    if (!selectedGroupID.value)
      selectPolicy(
        policies.value[0]?.group_id ?? eligibleGroups.value[0]?.id ?? 0,
      );
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    loading.value = false;
  }
}
function clone<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}
function selectPolicy(groupID: number) {
  selectedGroupID.value = groupID;
  const existing = policies.value.find((item) => item.group_id === groupID);
  const group = eligibleGroups.value.find((item) => item.id === groupID);
  policyForm.value = existing
    ? clone(existing)
    : group
      ? {
          group_id: group.id,
          group_name: group.name,
          enabled: false,
          reply_phrase: "",
          templates: defaultGroupApplicationTemplates(),
        }
      : null;
  attachment.value = undefined;
}
function handleAttachment(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  if (
    file &&
    (file.type !== "application/pdf" || file.size > 10 * 1024 * 1024)
  ) {
    appStore.showError(t("admin.groupApplications.invalidPDF"));
    input.value = "";
    return;
  }
  attachment.value = file;
}
async function downloadAgreement() {
  if (!policyForm.value?.attachment_id) return;
  try {
    const blob = await groupApplicationsAdminAPI.downloadAttachment(
      policyForm.value.attachment_id,
    );
    downloadBlob(blob, policyForm.value.attachment_name || "agreement.pdf");
  } catch (error) {
    appStore.showError(errorMessage(error));
  }
}
function downloadBlob(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  document.body.appendChild(link);
  link.click();
  link.remove();
  queueMicrotask(() => URL.revokeObjectURL(url));
}
async function exportCommunications() {
  if (!selectedApplication.value) return;
  try {
    const id = selectedApplication.value.id;
    const blob = await groupApplicationsAdminAPI.exportCommunications(id);
    downloadBlob(blob, `group-application-${id}-communications.json`);
  } catch (error) {
    appStore.showError(errorMessage(error));
  }
}
async function savePolicy() {
  if (!policyForm.value) return;
  saving.value = true;
  try {
    const saved = await groupApplicationsAdminAPI.savePolicy(
      policyForm.value.group_id,
      policyForm.value,
      attachment.value,
    );
    const index = policies.value.findIndex(
      (item) => item.group_id === saved.group_id,
    );
    if (index >= 0) policies.value[index] = saved;
    else policies.value.push(saved);
    policyForm.value = clone(saved);
    attachment.value = undefined;
    appStore.showSuccess(t("common.saved"));
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    saving.value = false;
  }
}
async function loadIMAP() {
  loading.value = true;
  try {
    [imapForm.value, workerHealth.value] = await Promise.all([
      groupApplicationsAdminAPI.getIMAP(),
      groupApplicationsAdminAPI.workerStatus(),
    ]);
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    loading.value = false;
  }
}
async function saveIMAP() {
  if (!imapForm.value) return;
  saving.value = true;
  try {
    imapForm.value = await groupApplicationsAdminAPI.saveIMAP(imapForm.value);
    appStore.showSuccess(t("common.saved"));
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    saving.value = false;
  }
}
async function testIMAP() {
  testing.value = true;
  try {
    await groupApplicationsAdminAPI.testIMAP();
    appStore.showSuccess(t("admin.groupApplications.connectionOK"));
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    testing.value = false;
  }
}
async function reloadActiveTab() {
  if (activeTab.value === "applications") await loadApplications();
  else if (activeTab.value === "policies") await loadPolicies();
  else await loadIMAP();
}
async function approveSelected() {
  if (!selectedApplication.value) return;
  saving.value = true;
  try {
    await groupApplicationsAdminAPI.approve(selectedApplication.value.id);
    await openApplication(selectedApplication.value.id);
    await loadApplications();
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    saving.value = false;
  }
}
function openDecision(mode: "reject" | "revoke") {
  decisionMode.value = mode;
  decisionReason.value = "";
}
async function submitDecision() {
  if (
    !selectedApplication.value ||
    !decisionMode.value ||
    !decisionReason.value
  )
    return;
  saving.value = true;
  try {
    if (decisionMode.value === "reject")
      await groupApplicationsAdminAPI.reject(
        selectedApplication.value.id,
        decisionReason.value,
      );
    else
      await groupApplicationsAdminAPI.revoke(
        selectedApplication.value.id,
        decisionReason.value,
      );
    decisionMode.value = null;
    await openApplication(selectedApplication.value.id);
    await loadApplications();
  } catch (error) {
    appStore.showError(errorMessage(error));
  } finally {
    saving.value = false;
  }
}
async function resendApproval() {
  if (!selectedApplication.value) return;
  try {
    await groupApplicationsAdminAPI.resendApproval(
      selectedApplication.value.id,
    );
    await openApplication(selectedApplication.value.id);
  } catch (error) {
    appStore.showError(errorMessage(error));
  }
}
async function retryMail(outboxID: number) {
  if (!selectedApplication.value) return;
  try {
    await groupApplicationsAdminAPI.retryMail(
      selectedApplication.value.id,
      outboxID,
    );
    await openApplication(selectedApplication.value.id);
  } catch (error) {
    appStore.showError(errorMessage(error));
  }
}
function formatDate(value: string) {
  return new Date(value).toLocaleString();
}
function formatBytes(value: number) {
  return value < 1024 * 1024
    ? `${Math.round(value / 1024)} KiB`
    : `${(value / 1024 / 1024).toFixed(1)} MiB`;
}
function statusClass(status: GroupApplicationStatus) {
  return {
    pending: "bg-blue-100 text-blue-700",
    awaiting_reply: "bg-amber-100 text-amber-700",
    completed: "bg-green-100 text-green-700",
    rejected: "bg-red-100 text-red-700",
    revoked: "bg-gray-200 text-gray-700",
  }[status];
}
onMounted(async () => {
  await Promise.all([loadApplications(), loadPolicies(), loadIMAP()]);
});
</script>
