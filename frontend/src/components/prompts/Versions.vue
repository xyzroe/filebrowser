<template>
  <div class="card floating" id="versions">
    <div class="card-title">
      <h2>{{ $t("locking.versionsTitle") }}</h2>
    </div>

    <div class="card-content">
      <p class="break-word">{{ $t("locking.versionsNoRestoreHint") }}</p>

      <p v-if="loading">{{ $t("files.loading") }}</p>
      <p v-else-if="versions.length === 0">
        {{ $t("locking.versionsEmpty") }}
      </p>

      <table v-else>
        <tr>
          <th>#</th>
          <th></th>
          <th>{{ $t("prompts.size") }}</th>
          <th>{{ $t("prompts.lastModified") }}</th>
          <th>{{ $t("prompts.displayName") }}</th>
          <th></th>
        </tr>
        <tr v-for="v in versions" :key="v.versionNumber">
          <td>{{ v.versionNumber }}</td>
          <td>
            <span v-if="v.isCurrent" class="counter">{{
              $t("locking.versionsCurrent")
            }}</span>
          </td>
          <td class="small">{{ humanSize(v.size) }}</td>
          <td class="small" :title="v.createdAt">
            {{ humanTime(v.createdAt) }}
          </td>
          <td class="break-word">{{ v.comment }}</td>
          <td class="small">
            <button
              class="action"
              :aria-label="$t('buttons.downloadVersion')"
              :title="$t('buttons.downloadVersion')"
              @click="download(v)"
            >
              <i class="material-icons">file_download</i>
            </button>
          </td>
        </tr>
      </table>
    </div>

    <div class="card-action">
      <button
        class="button button--flat button--grey"
        @click="closeHovers"
        :aria-label="$t('buttons.close')"
        :title="$t('buttons.close')"
      >
        {{ $t("buttons.close") }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, inject, onMounted } from "vue";
import { useLayoutStore } from "@/stores/layout";
import { useFileStore } from "@/stores/file";
import { versioning as api } from "@/api";
import type { VersionInfo } from "@/api/versioning";
import { filesize } from "@/utils";
import dayjs from "dayjs";

const layoutStore = useLayoutStore();
const fileStore = useFileStore();
const $showError = inject<IToastError>("$showError")!;

const versions = ref<VersionInfo[]>([]);
const loading = ref(true);
const isOwner = ref(false);

const closeHovers = () => layoutStore.closeHovers();

const selectedPath = () => {
  if (fileStore.selectedCount === 1 && fileStore.req) {
    return fileStore.req.items[fileStore.selected[0]].path;
  }
  return fileStore.req?.path ?? "/";
};

onMounted(async () => {
  try {
    const res = await api.listVersions(selectedPath());
    versions.value = res.versions;
    isOwner.value = !!res.lock?.isCurrentUserOwner;
  } catch (e) {
    $showError(e as Error);
  } finally {
    loading.value = false;
  }
});

const humanSize = (size: number) => filesize(size);
const humanTime = (iso: string) => dayjs(iso).format("L LT");

const download = async (v: VersionInfo) => {
  try {
    await api.downloadHistoricalVersion(
      selectedPath(),
      v.versionNumber,
      isOwner.value
    );
  } catch (e) {
    $showError(e as Error);
  }
};
</script>
