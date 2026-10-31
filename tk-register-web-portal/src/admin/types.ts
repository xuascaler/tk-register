import type { Submission, SubmissionDetail } from '../submissions/types';

export type AdminUser = {
  id: string;
  email: string;
  role: 'member' | 'admin';
  status: string;
  created_at: string;
};
export type Page<T> = { items: T[]; total: number; page: number; page_size: number };
export type Owner = { id: string; email: string };
export type AdminAccount = {
  id: string;
  account: string;
  user: Owner;
  vendor_id: string;
  vendor_name: string;
  plan: string;
  submission_count: number;
  latest_submission: Submission;
  current_submission: Submission | null;
};
export type AdminSubmission = Submission & {
  user: Owner;
  account_id: string;
  current_submission: Submission | null;
};
export type AdminDetail = SubmissionDetail & {
  user: Owner;
  account_id: string;
  current_submission: Submission | null;
};
export type AccountDetail = { account: AdminAccount; history: Page<AdminSubmission> };
