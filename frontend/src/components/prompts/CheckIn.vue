<template>
  <div class="card floating">
    <div class="card-title">
      <h2>{{ $t("locking.checkinTitle") }}</h2>
    </div>

    <div class="card-content">
      <p>{{ $t("locking.checkinMessage") }}</p>

      <p>
        <label class="button button--flat" for="checkin-file-input">
          {{ $t("locking.checkinSelectFile") }}
        </label>
        <input
          id="checkin-file-input"
          type="file"
          style="display: none"
          @change="onFileChange"
        />
      </p>

      <p v-if="selectedFile">
        <strong>{{ selectedFile.name }}</strong> ({{ humanSize }})
      </p>

      <p v-if="nameDiffers" class="break-word">
        {{
          $t("locking.checkinDifferentNameWarning", {
            name: selectedFile?.name,
          })
        }}
      </p>

      <p>
        <label for="checkin-comment">
          {{
            requireComment
              ? $t("locking.checkinCommentRequired")
              : $t("locking.checkinCommentLabel")
          }}
        </label>
      </p>
      <input
        id="checkin-comment"
        class="input input--block"
        type="text"
        v-model="comment"
      />

      <p v-if="uploading">{{ $t("locking.checkinInProgress") }}</p>
    </div>

    <div class="card-action">
      <button
        class="button button--flat button--grey"
        @click="closeHovers"
        :disabled="uploading"
        :aria-label="$t('buttons.cancel')"
        :title="$t('buttons.cancel')"
      >
        {{ $t("buttons.cancel") }}
      </button>
      <button
        id="focus-prompt"
        class="button button--flat button--blue"
        @click="submit"
        :disabled="
          !selectedFile || uploading || (requireComment && !comment.trim())
        "
        :aria-label="$t('locking.checkinTitle')"
        :title="$t('locking.checkinTitle')"
      >
        {{ $t("locking.checkinTitle") }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, inject, onMounted } from "vue";
import { useI18n } from "vue-i18n";
import { useLayoutStore } from "@/stores/layout";
import { useFileStore } from "@/stores/file";
import { versioning as api } from "@/api";
import { filesize } from "@/utils";
import { requireCheckinComment } from "@/utils/constants";

const layoutStore = useLayoutStore();
const fileStore = useFileStore();
const { t } = useI18n();
const $showError = inject<IToastError>("$showError")!;
const $showSuccess = inject<IToastSuccess>("$showSuccess")!;

const selectedFile = ref<File | null>(null);
const comment = ref("");
const uploading = ref(false);
const requireComment = requireCheckinComment;
const currentVersion = ref<number | null>(null);

const closeHovers = () => layoutStore.closeHovers();

const selected = () => {
  if (fileStore.selectedCount === 1 && fileStore.req) {
    return fileStore.req.items[fileStore.selected[0]];
  }
  return fileStore.req ?? null;
};

onMounted(async () => {
  const item = selected();
  if (!item) return;
  try {
    const lock = await api.getLock(item.path);
    currentVersion.value = lock.currentVersion ?? 0;
  } catch (e) {
    $showError(e as Error);
  }
});

const humanSize = computed(() =>
  selectedFile.value ? filesize(selectedFile.value.size) : ""
);

const nameDiffers = computed(() => {
  const item = selected();
  return (
    !!selectedFile.value && !!item && selectedFile.value.name !== item.name
  );
});

const onFileChange = (event: Event) => {
  const input = event.target as HTMLInputElement;
  selectedFile.value = input.files && input.files[0] ? input.files[0] : null;
};

const submit = async () => {
  const item = selected();
  if (!selectedFile.value || !item || currentVersion.value === null) return;

  uploading.value = true;
  try {
    await api.checkin(
      item.path,
      selectedFile.value,
      currentVersion.value,
      comment.value
    );
    $showSuccess(t("success.checkedIn"));
    fileStore.reload = true;
    closeHovers();
  } catch (e) {
    $showError(e as Error);
  } finally {
    uploading.value = false;
  }
};
</script>
