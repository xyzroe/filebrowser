<template>
  <div class="card floating">
    <div class="card-title">
      <h2>{{ $t("locking.checkoutTitle") }}</h2>
    </div>

    <div class="card-content">
      <p>{{ $t("locking.checkoutMessage") }}</p>
      <p>
        <label for="checkout-comment">
          {{
            requireComment
              ? $t("locking.checkoutCommentRequired")
              : $t("locking.checkoutCommentLabel")
          }}
        </label>
      </p>
      <input
        id="checkout-comment"
        class="input input--block"
        type="text"
        v-model="comment"
        tabindex="1"
      />
    </div>

    <div class="card-action">
      <button
        class="button button--flat button--grey"
        @click="closeHovers"
        :aria-label="$t('buttons.cancel')"
        :title="$t('buttons.cancel')"
        tabindex="3"
      >
        {{ $t("buttons.cancel") }}
      </button>
      <button
        id="focus-prompt"
        class="button button--flat button--blue"
        @click="submit"
        :disabled="requireComment && !comment.trim()"
        :aria-label="$t('locking.checkoutTitle')"
        :title="$t('locking.checkoutTitle')"
        tabindex="2"
      >
        {{ $t("locking.checkoutTitle") }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, inject } from "vue";
import { useLayoutStore } from "@/stores/layout";
import { useFileStore } from "@/stores/file";
import { versioning as api } from "@/api";
import { requireCheckoutComment } from "@/utils/constants";

const layoutStore = useLayoutStore();
const fileStore = useFileStore();
const $showError = inject<IToastError>("$showError")!;

const comment = ref("");
const requireComment = requireCheckoutComment;

const closeHovers = () => layoutStore.closeHovers();

const selectedPath = () => {
  if (fileStore.selectedCount === 1 && fileStore.req) {
    return fileStore.req.items[fileStore.selected[0]].path;
  }
  return fileStore.req?.path ?? "/";
};

const submit = async () => {
  try {
    await api.takeForWork(selectedPath(), comment.value);
    closeHovers();
  } catch (e) {
    $showError(e as Error);
  }
};
</script>
