<template>
  <div class="card floating">
    <div class="card-title">
      <h2>{{ $t("locking.cancelCheckoutTitle") }}</h2>
    </div>

    <div class="card-content">
      <p>{{ $t("locking.cancelCheckoutMessage") }}</p>
      <p>
        <label for="cancel-checkout-reason">{{
          $t("locking.cancelCheckoutReasonLabel")
        }}</label>
      </p>
      <input
        id="cancel-checkout-reason"
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
        :aria-label="$t('buttons.cancelCheckout')"
        :title="$t('buttons.cancelCheckout')"
      >
        {{ $t("buttons.cancelCheckout") }}
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
    await api.cancelCheckout(selectedPath(), reason.value);
    $showSuccess(t("success.checkoutCancelled"));
    fileStore.reload = true;
    closeHovers();
  } catch (e) {
    $showError(e as Error);
  }
};
</script>
