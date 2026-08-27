import { apiClient } from "./client";

export type GroupApplicationStatus =
  "pending" | "awaiting_reply" | "completed" | "rejected" | "revoked";

export interface GroupApplicationOption {
  group_id: number;
  group_name: string;
  description?: string;
  has_active: boolean;
  already_completed: boolean;
}

export interface GroupApplication {
  id: number;
  user_id: number;
  user_email?: string;
  group_id: number;
  group_name: string;
  contact_email: string;
  reason: string;
  locale: string;
  status: GroupApplicationStatus;
  attachment_id: number;
  attachment_name?: string;
  decision_reason?: string;
  last_email_kind?: string;
  last_email_status?: string;
  last_email_error?: string;
  reviewed_at?: string;
  completed_at?: string;
  revoked_at?: string;
  created_at: string;
  updated_at: string;
}

export interface GroupApplicationSummary {
  available_count: number;
  active_count: number;
  has_history: boolean;
}

export const groupApplicationsAPI = {
  async summary(): Promise<GroupApplicationSummary> {
    const { data } = await apiClient.get<GroupApplicationSummary>(
      "/group-applications/summary",
    );
    return data;
  },
  async options(): Promise<GroupApplicationOption[]> {
    const { data } = await apiClient.get<GroupApplicationOption[]>(
      "/group-applications/options",
    );
    return data;
  },
  async list(): Promise<GroupApplication[]> {
    const { data } = await apiClient.get<GroupApplication[]>(
      "/group-applications",
    );
    return data;
  },
  async create(input: {
    group_id: number;
    contact_email: string;
    reason: string;
    locale: string;
  }): Promise<GroupApplication> {
    const { data } = await apiClient.post<GroupApplication>(
      "/group-applications",
      input,
    );
    return data;
  },
};

export default groupApplicationsAPI;
