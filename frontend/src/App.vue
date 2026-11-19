<template>
  <router-view v-if="isStatsOverlay" />
  <template v-else>
    <MainLayout />
    <GuidedTour />
    <RuntimeHealthBanner />
    <MessageProvider />
    <AdModelProvider v-if="isMainWindow" />
    <Modal
      :visible="modalState.visible"
      :title="modalState.title"
      :content="modalState.content"
      :confirm-text="modalState.confirmText"
      :cancel-text="modalState.cancelText"
      :show-cancel="modalState.showCancel"
      :confirm-disabled="modalState.confirmDisabled"
      @confirm="resolveModal(true)"
      @cancel="resolveModal(false)"
    />
    <InputModal
      :visible="inputModalState.visible"
      :title="inputModalState.title"
      :content="inputModalState.content"
      :placeholder="inputModalState.placeholder"
      :model-value="inputModalState.value"
      @update:model-value="inputModalState.value = $event"
      @confirm="resolveInputModal(true)"
      @cancel="resolveInputModal(false)"
    />
  </template>
</template>
<script setup>
import MainLayout from "@/layouts/MainLayout.vue";
import GuidedTour from "@/components/onboarding/GuidedTour.vue";
import RuntimeHealthBanner from "@/components/RuntimeHealthBanner.vue";
import AdModelProvider from "@/components/AdModelProvider.vue";
import Modal from "@/components/ui/Modal.vue";
import MessageProvider from "@/components/ui/MessageProvider.vue";
import { modalState, resolveModal } from "@/composables/useModal";

import InputModal from "@/components/ui/InputModal.vue";
import { inputModalState, resolveInputModal } from "@/composables/useInputModal";
import { computed } from "vue";
import { useRoute } from "vue-router";

const route = useRoute();
const isMainWindow = computed(() => route.path === "/");
const isStatsOverlay = computed(() => route.path === "/stats-overlay");
</script>
