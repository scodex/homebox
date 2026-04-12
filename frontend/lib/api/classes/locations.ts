import { BaseAPI, route } from "../base";
import type { LocationCreate, LocationOut, LocationOutCount, LocationUpdate, TreeItem } from "../types/data-contracts";

export type LocationsQuery = {
  filterChildren: boolean;
};

export type TreeQuery = {
  withItems: boolean;
};

export interface FloorPlanPositionUpdate {
  id: string;
  x: number;
  y: number;
}

export interface FloorPlanPositionsUpdateRequest {
  locations: FloorPlanPositionUpdate[];
  items: FloorPlanPositionUpdate[];
}

export class LocationsApi extends BaseAPI {
  getAll(q: LocationsQuery = { filterChildren: false }) {
    return this.http.get<LocationOutCount[]>({ url: route("/locations", q) });
  }

  getTree(tq = { withItems: false }) {
    return this.http.get<TreeItem[]>({ url: route("/locations/tree", tq) });
  }

  create(body: LocationCreate) {
    return this.http.post<LocationCreate, LocationOut>({ url: route("/locations"), body });
  }

  get(id: string) {
    return this.http.get<LocationOut>({ url: route(`/locations/${id}`) });
  }

  delete(id: string) {
    return this.http.delete<void>({ url: route(`/locations/${id}`) });
  }

  update(id: string, body: LocationUpdate) {
    return this.http.put<LocationUpdate, LocationOut>({ url: route(`/locations/${id}`), body });
  }

  // Floor plan methods
  uploadFloorPlan(id: string, file: File) {
    const formData = new FormData();
    formData.append("file", file);
    return this.http.post<FormData, LocationOut>({
      url: route(`/locations/${id}/floor-plan`),
      data: formData,
    });
  }

  deleteFloorPlan(id: string) {
    return this.http.delete<void>({ url: route(`/locations/${id}/floor-plan`) });
  }

  getFloorPlanImageUrl(id: string) {
    return route(`/locations/${id}/floor-plan/image`);
  }

  updateFloorPlanPositions(id: string, body: FloorPlanPositionsUpdateRequest) {
    return this.http.put<FloorPlanPositionsUpdateRequest, void>({
      url: route(`/locations/${id}/floor-plan/positions`),
      body,
    });
  }
}
