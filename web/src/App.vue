<script setup lang="ts">
import { RouterView } from 'vue-router'
import { CheckCircle2, CircleAlert, Info, X } from 'lucide-vue-next'
import { toast } from '@/state/toast'
</script>

<template>
  <RouterView v-slot="{ Component }">
    <Transition name="page" mode="out-in">
      <component :is="Component" />
    </Transition>
  </RouterView>

  <div class="toast-stack" aria-live="polite" aria-atomic="false">
    <TransitionGroup name="toast">
      <section v-for="item in toast.items" :key="item.id" class="toast-item" :class="`toast-${item.type}`">
        <CheckCircle2 v-if="item.type === 'success'" :size="19" />
        <CircleAlert v-else-if="item.type === 'error'" :size="19" />
        <Info v-else :size="19" />
        <div>
          <strong>{{ item.title }}</strong>
          <p v-if="item.message">{{ item.message }}</p>
        </div>
        <button class="icon-button subtle" type="button" aria-label="关闭通知" @click="toast.dismiss(item.id)">
          <X :size="16" />
        </button>
      </section>
    </TransitionGroup>
  </div>
</template>
