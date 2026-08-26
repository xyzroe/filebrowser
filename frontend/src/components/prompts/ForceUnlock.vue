<template>
  <div class="card floating">
    <div class="card-title">
      <h2>{{ $t("locking.forceUnlockTitle") }}</h2>
    </div>

    <div class="card-content">
      <p>{{ $t("locking.forceUnlockMessage") }}</p>
      <p>
        <label for="force-unlock-reason">{{
          $t("locking.forceUnlockReasonLabel")
        }}</label>
      </p>
      <input
        id="force-unlock-reason"
        class="input input--block"
        type="text"
        v-model="reason"
      />
    </div>

    <div class="card-action">
      <button
        class="button button--flat button--grey"
        @click="closeHovers"
        :aria-label="$t('buttons.cancel')"
        :title="$t('buttons.cancel')"
      >
        {{ $t("buttons.cancel") }}
      </button>
      <button
        id="focus-prompt"
        class="button button--flat button--blue"
        :disabled="!reason.trim()"
        @click="submit"
        :aria-label="$t('buttons.forceUnlock')"
        :title="$t('buttons.forceUnlock')"
      >
        {{ $t("buttons.forceUnlock") }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, inject } from "vue";
import { useI18n } from "vue-i18n";
import { useLayoutStore } from "@/stores/layout";
import { useFileStore } from "@/stores/file";
import { versioning as api } from "@/api";

const layoutStore = useLayoutStore();
const fileStore = useFileStore();
const { t } = useI18n();
const $showError = inject<IToastError>("$showError")!;
const $showSuccess = inject<IToastSuccess>("$showSuccess")!;

const reason = ref("");

const closeHovers = () => layoutStore.closeHovers();

const selectedPath = () => {
  if (fileStore.selectedCount === 1 && fileStore.req) {
    return fileStore.req.items[fileStore.selected[0]].path;
  }
  return fileStore.req?.path ?? "/";
};

const submit = async () => {
  try {
    await api.forceUnlock(selectedPath(), reason.value);
    $showSuccess(t("success.forceUnlocked"));
    fileStore.reload = true;
    closeHovers();
  } catch (e) {
    $showError(e as Error);
  }
};
</script>
