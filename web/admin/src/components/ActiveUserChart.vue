<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { Line } from 'vue-chartjs'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Filler,
  type ChartData,
  type ChartOptions,
} from 'chart.js'
import { adminClient } from '@/transport/adminClient'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Filler)

const props = defineProps<{
  from?: Date
  to?: Date
}>()

const loading = ref(true)
const error = ref<string | null>(null)
const hasData = ref(false)
const chartData = ref<ChartData<'line'>>({ labels: [], datasets: [] })

const options = computed<ChartOptions<'line'>>(() => ({
  responsive: true,
  maintainAspectRatio: true,
  aspectRatio: 3 / 2,
  plugins: {
    legend: { display: false },
    tooltip: {
      backgroundColor: 'var(--color-surface-container-high)',
      titleColor: 'var(--color-on-surface)',
      bodyColor: 'var(--color-on-surface-variant)',
      borderColor: 'var(--color-outline-variant)',
      borderWidth: 1,
    },
  },
  scales: {
    x: {
      grid: { display: false },
      ticks: { color: 'var(--color-on-surface-variant)', font: { size: 11 } },
    },
    y: {
      beginAtZero: true,
      grid: { color: 'var(--color-outline-variant)', drawBorder: false },
      ticks: {
        color: 'var(--color-on-surface-variant)',
        font: { size: 11 },
        stepSize: 1,
        callback: (value) => (Number.isInteger(value) ? value : ''),
      },
    },
  },
}))

async function fetchData() {
  loading.value = true
  error.value = null
  try {
    const resp = await adminClient().getActiveUserChart({
      fromDate: props.from
        ? { seconds: BigInt(Math.floor(props.from.getTime() / 1000)) }
        : undefined,
      toDate: props.to ? { seconds: BigInt(Math.floor(props.to.getTime() / 1000)) } : undefined,
    })
    hasData.value = resp.hasData
    if (resp.hasData && resp.days.length > 0) {
      chartData.value = {
        labels: resp.days.map((d) => d.date),
        datasets: [
          {
            label: 'Active Users',
            data: resp.days.map((d) => d.activeUserCount),
            borderColor: 'var(--color-primary)',
            // Canvas fill uses hard-coded rgba because CSS variables cannot carry alpha in Chart.js.
            backgroundColor: 'rgba(38, 66, 230, 0.1)',
            fill: true,
            tension: 0.3,
            pointRadius: 2,
            pointHoverRadius: 4,
            borderWidth: 2,
          },
        ],
      }
    } else {
      chartData.value = { labels: [], datasets: [] }
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Failed to load chart data'
  } finally {
    loading.value = false
  }
}

onMounted(fetchData)
watch(() => [props.from, props.to], fetchData)
</script>

<template>
  <div class="rounded-xl border border-outline-variant bg-surface-container-lowest p-5">
    <h3 class="text-sm font-semibold text-on-surface">Active Users</h3>
    <p class="mt-0.5 text-xs text-on-surface-variant">
      Distinct users who made tool calls each day
    </p>

    <!-- Loading skeleton -->
    <div v-if="loading" class="mt-4 animate-pulse">
      <div class="h-48 rounded-lg bg-surface-container" />
    </div>

    <!-- Error state -->
    <div v-else-if="error" class="mt-4 rounded-lg bg-error-container p-4 text-sm text-error">
      {{ error }}
    </div>

    <!-- Empty state -->
    <div
      v-else-if="!hasData"
      class="mt-4 flex h-48 items-center justify-center rounded-lg border border-dashed border-outline-variant"
    >
      <p class="text-sm text-on-surface-variant">No data yet</p>
    </div>

    <!-- Chart -->
    <div v-else class="mt-4">
      <Line :data="chartData" :options="options" />
    </div>
  </div>
</template>
