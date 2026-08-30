import { useAuthStore } from "@/stores/auth";
import { baseURL } from "@/utils/constants";
import { fetchURL } from "./utils";

export interface LockOwner {
  id: number;
  username?: string;
}

export interface LockInfo {
  fileId: string;
  state: "unmanaged" | "available" | "locked";
  owner?: LockOwner;
  lockedAt?: string;
  checkoutVersion?: number;
  comment?: string;
  isCurrentUserOwner: boolean;
  currentVersion?: number;
}

export interface VersionInfo {
  versionNumber: number;
  createdAt: string;
  createdBy?: string;
  size: number;
  sha256: string;
  comment?: string;
  originalUploadName?: string;
  isCurrent: boolean;
}

export interface VersionsListResponse {
  fileId: string;
  currentVersion: number;
  lock?: LockInfo;
  versions: VersionInfo[];
}

// normalizePath ensures a plain resource path (e.g. ResourceItem.path,
// Resource.path) has a leading slash. Unlike files.ts's removePrefix, it must
// NOT strip the first two path segments: these paths never carry the
// frontend's "/files" route prefix to begin with, so removePrefix would
// mangle e.g. "/backup.zip" down to "/".
function normalizePath(path: string): string {
  return path.startsWith("/") ? path : "/" + path;
}

function apiPath(
  endpoint: string,
  path: string,
  extra: Record<string, string> = {}
) {
  const params = new URLSearchParams({
    path: normalizePath(path),
    ...extra,
  });
  return `/api/${endpoint}?${params.toString()}`;
}

export async function getLock(path: string): Promise<LockInfo> {
  const res = await fetchURL(apiPath("resources/lock", path), {});
  return (await res.json()) as LockInfo;
}

export interface MyLock {
  path: string;
  lockedAt: string;
}

// myLocks lists every file the current user has checked out, for the
// sidebar's "locked by you" summary.
export async function myLocks(): Promise<MyLock[]> {
  const res = await fetchURL(`/api/locks/mine`, {});
  const data = (await res.json()) as { locks: MyLock[] };
  return data.locks;
}

export async function listVersions(path: string): Promise<VersionsListResponse> {
  const res = await fetchURL(apiPath("resources/versions", path), {});
  return (await res.json()) as VersionsListResponse;
}

export async function checkout(
  path: string,
  comment: string
): Promise<{ token: string; fileId: string; checkoutVersion: number }> {
  const res = await fetchURL(apiPath("resources/checkout", path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ comment }),
  });
  return await res.json();
}

export async function checkoutVersion(
  path: string,
  version: number,
  comment: string
): Promise<{ token: string; fileId: string; versionNumber: number }> {
  const res = await fetchURL(
    apiPath("resources/versions/checkout", path, { version: String(version) }),
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ comment }),
    }
  );
  return await res.json();
}

export async function cancelCheckout(path: string, reason: string): Promise<void> {
  await fetchURL(apiPath("resources/checkout/cancel", path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ reason }),
  });
}

export async function forceUnlock(path: string, reason: string): Promise<void> {
  await fetchURL(apiPath("admin/resources/unlock", path), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ reason }),
  });
}

// downloadCurrentURL/downloadVersionURL build plain GET URLs meant for
// window.open(), mirroring files.ts's getDownloadURL. The caller must already
// hold the lock (owner) — see takeForWork()/downloadVersion() below, which
// call checkout first when needed.
export function downloadCurrentURL(path: string): string {
  return `${baseURL}/api/raw${normalizePath(path)}`;
}

export function downloadVersionURL(path: string, version: number): string {
  return `${baseURL}${apiPath("resources/versions/download", path, {
    version: String(version),
  })}`;
}

// takeForWork performs the atomic checkout (creating the lock) and then opens
// the current version for download — the "Take for work" action.
export async function takeForWork(path: string, comment: string): Promise<void> {
  await checkout(path, comment);
  window.open(downloadCurrentURL(path));
}

// downloadAgain re-downloads the current version without a new checkout; only
// valid while the requesting user already owns the lock.
export function downloadAgain(path: string): void {
  window.open(downloadCurrentURL(path));
}

// downloadHistoricalVersion checks out (if not already owned) the given
// historical version and opens it for download.
export async function downloadHistoricalVersion(
  path: string,
  version: number,
  alreadyOwned: boolean,
  comment = ""
): Promise<void> {
  if (!alreadyOwned) {
    await checkoutVersion(path, version, comment);
  }
  window.open(downloadVersionURL(path, version));
}

export async function checkin(
  path: string,
  file: File,
  expectedCurrentVersion: number,
  comment: string,
  onProgress?: (event: ProgressEvent) => void
): Promise<{ versionNumber: number }> {
  const authStore = useAuthStore();
  const formData = new FormData();
  formData.append("file", file, file.name);
  formData.append("expectedCurrentVersion", String(expectedCurrentVersion));
  formData.append("comment", comment);

  return new Promise((resolve, reject) => {
    const request = new XMLHttpRequest();
    request.open("POST", `${baseURL}${apiPath("resources/checkin", path)}`, true);
    request.setRequestHeader("X-Auth", authStore.jwt);

    if (onProgress) {
      request.upload.onprogress = onProgress;
    }

    request.onload = () => {
      if (request.status >= 200 && request.status < 300) {
        resolve(JSON.parse(request.responseText));
      } else {
        reject(new Error(request.responseText || `${request.status}`));
      }
    };
    request.onerror = () => reject(new Error("001 Connection aborted"));
    request.send(formData);
  });
}
