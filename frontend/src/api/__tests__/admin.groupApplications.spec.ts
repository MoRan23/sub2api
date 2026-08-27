import { beforeEach, describe, expect, it, vi } from "vitest";

const { put } = vi.hoisted(() => ({
  put: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  apiClient: { put },
}));

import {
  defaultGroupApplicationTemplates,
  groupApplicationsAdminAPI,
  type GroupApplicationPolicy,
} from "@/api/admin/groupApplications";

describe("group application policy API", () => {
  beforeEach(() => {
    put.mockReset();
  });

  it("sends a selected agreement as multipart instead of the client's default JSON body", async () => {
    const policy: GroupApplicationPolicy = {
      group_id: 4,
      group_name: "Private Pro",
      enabled: true,
      reply_phrase: "EXACT-CONFIRM",
      templates: defaultGroupApplicationTemplates(),
    };
    const agreement = new File(["%PDF-1.7\nfixture"], "agreement-v2.pdf", {
      type: "application/pdf",
    });
    put.mockResolvedValue({ data: policy });

    await groupApplicationsAdminAPI.savePolicy(4, policy, agreement);

    expect(put).toHaveBeenCalledTimes(1);
    const [url, body, config] = put.mock.calls[0];
    expect(url).toBe("/admin/group-application-policies/4");
    expect(body).toBeInstanceOf(FormData);
    expect(JSON.parse(String(body.get("policy")))).toMatchObject({
      enabled: true,
      reply_phrase: "EXACT-CONFIRM",
    });
    expect((body.get("attachment") as File).name).toBe("agreement-v2.pdf");
    expect(config).toEqual({ headers: { "Content-Type": "multipart/form-data" } });
  });
});
