<script setup lang="ts">
  import { ref, computed, onMounted } from "vue";
  import { useI18n } from "vue-i18n";
  import { toast } from "@/components/ui/sonner";
  import type { LocationSummary, ItemSummary } from "~~/lib/api/types/data-contracts";
  import type { FloorPlanPositionUpdate, FloorPlanPositionsUpdateRequest } from "~~/lib/api/classes/locations";
  import MdiMapMarkerOutline from "~icons/mdi/map-marker-outline";
  import MdiPackageVariant from "~icons/mdi/package-variant";
  import MdiCursorMove from "~icons/mdi/cursor-move";
  import MdiContentSave from "~icons/mdi/content-save";
  import MdiUpload from "~icons/mdi/upload";
  import MdiDelete from "~icons/mdi/delete";
  import MdiEye from "~icons/mdi/eye";
  import { Button } from "@/components/ui/button";
  import { Card } from "@/components/ui/card";

  const { t } = useI18n();
  const api = useUserApi();

  const props = defineProps<{
    locationId: string;
    floorPlanPath: string;
    children: LocationSummary[];
    items: ItemSummary[];
  }>();

  const emit = defineEmits<{
    (e: "refresh"): void;
  }>();

  const editMode = ref(false);
  const containerRef = ref<HTMLDivElement | null>(null);
  const imageLoaded = ref(false);
  const dragTarget = ref<{ type: "location" | "item"; id: string } | null>(null);
  const saving = ref(false);

  // Local copies of positions for drag editing
  const locationPositions = ref<Map<string, { x: number; y: number }>>(new Map());
  const itemPositions = ref<Map<string, { x: number; y: number }>>(new Map());

  const floorPlanImageUrl = computed(() => {
    if (!props.floorPlanPath) return "";
    return `/api/v1/locations/${props.locationId}/floor-plan/image`;
  });

  function initPositions() {
    const lp = new Map<string, { x: number; y: number }>();
    for (const child of props.children) {
      lp.set(child.id, { x: child.floorPlanX || 0, y: child.floorPlanY || 0 });
    }
    locationPositions.value = lp;

    const ip = new Map<string, { x: number; y: number }>();
    for (const item of props.items) {
      ip.set(item.id, { x: item.floorPlanX || 0, y: item.floorPlanY || 0 });
    }
    itemPositions.value = ip;
  }

  onMounted(initPositions);

  // Watch for prop changes
  watch(() => [props.children, props.items], initPositions, { deep: true });

  function getMarkerStyle(x: number, y: number) {
    return {
      left: `${x}%`,
      top: `${y}%`,
      transform: "translate(-50%, -100%)",
    };
  }

  function onMouseDown(type: "location" | "item", id: string, e: MouseEvent) {
    if (!editMode.value) return;
    e.preventDefault();
    dragTarget.value = { type, id };

    const onMouseMove = (ev: MouseEvent) => {
      updatePosition(ev.clientX, ev.clientY);
    };

    const onMouseUp = () => {
      dragTarget.value = null;
      window.removeEventListener("mousemove", onMouseMove);
      window.removeEventListener("mouseup", onMouseUp);
    };

    window.addEventListener("mousemove", onMouseMove);
    window.addEventListener("mouseup", onMouseUp);
  }

  function onTouchStart(type: "location" | "item", id: string, e: TouchEvent) {
    if (!editMode.value) return;
    e.preventDefault();
    dragTarget.value = { type, id };

    const onTouchMove = (ev: TouchEvent) => {
      const touch = ev.touches[0];
      updatePosition(touch.clientX, touch.clientY);
    };

    const onTouchEnd = () => {
      dragTarget.value = null;
      window.removeEventListener("touchmove", onTouchMove);
      window.removeEventListener("touchend", onTouchEnd);
    };

    window.addEventListener("touchmove", onTouchMove, { passive: false });
    window.addEventListener("touchend", onTouchEnd);
  }

  function updatePosition(clientX: number, clientY: number) {
    if (!dragTarget.value || !containerRef.value) return;

    const rect = containerRef.value.getBoundingClientRect();
    let x = ((clientX - rect.left) / rect.width) * 100;
    let y = ((clientY - rect.top) / rect.height) * 100;

    // Clamp 0-100
    x = Math.max(0, Math.min(100, x));
    y = Math.max(0, Math.min(100, y));

    const { type, id } = dragTarget.value;
    if (type === "location") {
      locationPositions.value.set(id, { x, y });
    } else {
      itemPositions.value.set(id, { x, y });
    }
  }

  async function savePositions() {
    saving.value = true;

    const locations: FloorPlanPositionUpdate[] = [];
    for (const [id, pos] of locationPositions.value) {
      locations.push({ id, x: Math.round(pos.x * 100) / 100, y: Math.round(pos.y * 100) / 100 });
    }

    const items: FloorPlanPositionUpdate[] = [];
    for (const [id, pos] of itemPositions.value) {
      items.push({ id, x: Math.round(pos.x * 100) / 100, y: Math.round(pos.y * 100) / 100 });
    }

    const body: FloorPlanPositionsUpdateRequest = { locations, items };
    const { error } = await api.locations.updateFloorPlanPositions(props.locationId, body);

    if (error) {
      toast.error(t("locations.floor_plan.save_error"));
    } else {
      toast.success(t("locations.floor_plan.saved"));
      editMode.value = false;
      emit("refresh");
    }

    saving.value = false;
  }

  async function handleUpload(event: Event) {
    const target = event.target as HTMLInputElement;
    if (!target.files || target.files.length === 0) return;

    const file = target.files[0];
    const { error } = await api.locations.uploadFloorPlan(props.locationId, file);

    if (error) {
      toast.error(t("locations.floor_plan.upload_error"));
    } else {
      toast.success(t("locations.floor_plan.uploaded"));
      emit("refresh");
    }

    target.value = "";
  }

  async function handleDelete() {
    const { error } = await api.locations.deleteFloorPlan(props.locationId);

    if (error) {
      toast.error(t("locations.floor_plan.delete_error"));
    } else {
      toast.success(t("locations.floor_plan.deleted"));
      emit("refresh");
    }
  }

  function navigateToChild(type: "location" | "item", id: string) {
    if (editMode.value) return;
    if (type === "location") {
      navigateTo(`/location/${id}`);
    } else {
      navigateTo(`/item/${id}`);
    }
  }
</script>

<template>
  <Card class="mt-6 overflow-hidden">
    <div class="flex items-center justify-between border-b px-4 py-3">
      <h3 class="flex items-center gap-2 text-lg font-semibold">
        <MdiMapMarkerOutline class="size-5" />
        {{ $t("locations.floor_plan.title") }}
      </h3>
      <div class="flex items-center gap-2">
        <template v-if="floorPlanPath">
          <Button
            v-if="!editMode"
            size="sm"
            variant="outline"
            @click="editMode = true"
          >
            <MdiCursorMove class="mr-1 size-4" />
            {{ $t("locations.floor_plan.edit") }}
          </Button>
          <template v-else>
            <Button
              size="sm"
              :disabled="saving"
              @click="savePositions"
            >
              <MdiContentSave class="mr-1 size-4" />
              {{ $t("locations.floor_plan.save") }}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              @click="editMode = false; initPositions()"
            >
              {{ $t("global.cancel") }}
            </Button>
          </template>
          <Button
            size="sm"
            variant="destructive"
            @click="handleDelete"
          >
            <MdiDelete class="size-4" />
          </Button>
        </template>
        <label v-else>
          <Button size="sm" variant="outline" as="span" class="cursor-pointer">
            <MdiUpload class="mr-1 size-4" />
            {{ $t("locations.floor_plan.upload") }}
          </Button>
          <input
            type="file"
            class="hidden"
            accept="image/*"
            @change="handleUpload"
          />
        </label>
      </div>
    </div>

    <!-- Floor plan canvas -->
    <div
      v-if="floorPlanPath"
      ref="containerRef"
      class="relative select-none"
      :class="{ 'cursor-crosshair': editMode }"
    >
      <img
        :src="floorPlanImageUrl"
        class="block w-full"
        :class="{ 'opacity-60': editMode }"
        alt="Floor plan"
        @load="imageLoaded = true"
      />

      <!-- Child location markers -->
      <template v-if="imageLoaded">
        <div
          v-for="child in children"
          :key="'loc-' + child.id"
          class="absolute z-10 flex flex-col items-center"
          :style="getMarkerStyle(
            locationPositions.get(child.id)?.x ?? 0,
            locationPositions.get(child.id)?.y ?? 0
          )"
          :class="editMode ? 'cursor-grab active:cursor-grabbing' : 'cursor-pointer'"
          @mousedown="onMouseDown('location', child.id, $event)"
          @touchstart="onTouchStart('location', child.id, $event)"
          @click="navigateToChild('location', child.id)"
        >
          <div
            class="rounded-full bg-blue-500 p-1.5 text-white shadow-lg ring-2 ring-white transition-transform hover:scale-110"
          >
            <MdiPackageVariant class="size-4" />
          </div>
          <span
            class="mt-0.5 max-w-[120px] truncate rounded bg-blue-500/90 px-1.5 py-0.5 text-[10px] font-medium text-white shadow"
          >
            {{ child.name }}
          </span>
        </div>

        <!-- Item markers -->
        <div
          v-for="item in items"
          :key="'item-' + item.id"
          class="absolute z-10 flex flex-col items-center"
          :style="getMarkerStyle(
            itemPositions.get(item.id)?.x ?? 0,
            itemPositions.get(item.id)?.y ?? 0
          )"
          :class="editMode ? 'cursor-grab active:cursor-grabbing' : 'cursor-pointer'"
          @mousedown="onMouseDown('item', item.id, $event)"
          @touchstart="onTouchStart('item', item.id, $event)"
          @click="navigateToChild('item', item.id)"
        >
          <div
            class="rounded-full bg-emerald-500 p-1.5 text-white shadow-lg ring-2 ring-white transition-transform hover:scale-110"
          >
            <MdiEye class="size-4" />
          </div>
          <span
            class="mt-0.5 max-w-[120px] truncate rounded bg-emerald-500/90 px-1.5 py-0.5 text-[10px] font-medium text-white shadow"
          >
            {{ item.name }}
          </span>
        </div>
      </template>

      <!-- Edit mode overlay hint -->
      <div
        v-if="editMode"
        class="pointer-events-none absolute inset-0 flex items-center justify-center"
      >
        <div class="rounded-lg bg-black/60 px-4 py-2 text-sm text-white backdrop-blur-sm">
          {{ $t("locations.floor_plan.drag_hint") }}
        </div>
      </div>
    </div>

    <!-- Empty state -->
    <div
      v-else
      class="flex flex-col items-center justify-center gap-3 py-12 text-muted-foreground"
    >
      <MdiMapMarkerOutline class="size-12 opacity-30" />
      <p class="text-sm">{{ $t("locations.floor_plan.empty") }}</p>
      <label>
        <Button size="sm" variant="outline" as="span" class="cursor-pointer">
          <MdiUpload class="mr-1 size-4" />
          {{ $t("locations.floor_plan.upload") }}
        </Button>
        <input
          type="file"
          class="hidden"
          accept="image/*"
          @change="handleUpload"
        />
      </label>
    </div>
  </Card>
</template>
