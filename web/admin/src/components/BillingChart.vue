<script setup lang="ts">
import { computed, ref, watch } from 'vue'
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

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Filler)

function resolveCSSVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || '#000'
}

export interface ChartDayRecord {
  date: string
  value: number
}

const props = defineProps<{
  title: string
  description: string
  datasetLabel: string
  lineColor: string
  fillColor: string
  from?: Date
  to?: Date
  fetchDataFn: (params: {
    from?: Date
    to?: Date
  }) => Promise<{ hasData: boolean; days: Record<string, any>[] }>
  mapDataFn: (day: Record<string, any>) => number
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
      backgroundColor: resolveCSSVar('--color-surface-container-high'),
      titleColor: resolveCSSVar('--color-on-surface'),
      bodyColor: resolveCSSVar('--color-on-surface-variant'),
      borderColor: resolveCSSVar('--color-outline-variant'),
      borderWidth: 1,
    },
  },
  scales: {
    x: {
      grid: { display: false },
      ticks: { color: resolveCSSVar('--color-on-surface-variant'), font: { size: 11 } },
    },
    y: {
      beginAtZero: true,
      grid: { color: resolveCSSVar('--color-outline-variant'), drawBorder: false },
      ticks: {
        color: resolveCSSVar('--color-on-surface-variant'),
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
    const resp = await props.fetchDataFn({
      from: props.from,
      to: props.to,
    })
    hasData.value = resp.hasData
    if (resp.hasData && resp.days.length > 0) {
      chartData.value = {
        labels: resp.days.map((d) => d.date),
        datasets: [
          {
            label: props.datasetLabel,
            data: resp.days.map((d) => props.mapDataFn(d)),
            borderColor: props.lineColor,
            backgroundColor: props.fillColor,
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

watch(() => [props.from, props.to], fetchData, { immediate: true })
</script>

<template>
  <div class="rounded-xl border border-outline-variant bg-surface-container-lowest p-5">
    <h3 class="text-sm font-semibold text-on-surface">{{ title }}</h3>
    <p class="mt-0.5 text-xs text-on-surface-variant">
      {{ description }}
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
