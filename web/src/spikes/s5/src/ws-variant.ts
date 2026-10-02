// The wavesurfer.js 8 variant (BSD-3-Clause): waveform + regions + timeline + minimap over the same peaks, driven by
// the AudioView's external time axis (zoom + setScrollTime on every axis change), playing an MSE-backed element.
import WaveSurfer from "wavesurfer.js";
import Minimap from "wavesurfer.js/dist/plugins/minimap.esm.js";
import Regions from "wavesurfer.js/dist/plugins/regions.esm.js";
import Timeline from "wavesurfer.js/dist/plugins/timeline.esm.js";
import type { Peaks } from "./sources";

export type WsProbe = {
  canvases: number;
  blankCanvases: number;
  canvasesInContainerDoc: number;
  wrapperOwnerIsContainerDoc: boolean;
  regionStart: number;
  scrollPx: number;
  wrapperWidth: number;
  errors: string[];
};

export class WsVariant {
  readonly ws: WaveSurfer;
  readonly regions: ReturnType<typeof Regions.create>;
  region?: ReturnType<ReturnType<typeof Regions.create>["addRegion"]>;
  private pxPerSec = 0;
  errors: string[] = [];
  updates = 0;
  constructor(
    readonly container: HTMLElement,
    peaks: Peaks,
    duration: number,
    media: HTMLMediaElement,
  ) {
    // wavesurfer takes peaks as a downsampled signal per channel: interleave min and max at 10 ms.
    const base = peaks.levels[0]!;
    const ch = peaks.channels;
    const n = base.length / (2 * ch);
    const channelData = Array.from({ length: ch }, (_, c) => {
      const out = new Float32Array(2 * n);
      for (let i = 0; i < n; i++) {
        out[2 * i] = base[(i * ch + c) * 2]! / 127;
        out[2 * i + 1] = base[(i * ch + c) * 2 + 1]! / 127;
      }
      return out;
    });
    this.regions = Regions.create();
    this.ws = WaveSurfer.create({
      container,
      media,
      peaks: channelData,
      duration,
      height: 96,
      splitChannels: ch > 1 ? Array.from({ length: ch }, () => ({})) : undefined,
      hideScrollbar: true,
      autoScroll: false,
      autoCenter: false,
      interact: true,
      waveColor: "rgb(110, 120, 220)",
      progressColor: "rgb(70, 80, 180)",
      plugins: [this.regions, Timeline.create({ height: 18 }), Minimap.create({ height: 24, waveColor: "rgb(150,150,170)" })],
    });
    this.ws.on("error", (e) => this.errors.push(String(e)));
    // A region added before "ready" is clamped to a zero duration: add it once wavesurfer knows the duration.
    const add = () => {
      if (this.region) return;
      this.region = this.regions.addRegion({ start: Math.min(5, duration / 4), end: Math.min(8, duration / 3), color: "rgba(99, 102, 241, 0.25)", drag: true, resize: true });
      this.region.on("update", () => this.updates++);
    };
    if (this.ws.getDuration() > 0) add();
    else this.ws.once("ready", add);
  }
  /** Follow the AudioView's axis: zoom to width/span pixels per second, then scroll to the start. */
  follow(start: number, span: number) {
    const w = this.container.clientWidth || 1;
    const px = w / span;
    if (Math.abs(px - this.pxPerSec) > 1e-6) {
      this.pxPerSec = px;
      this.ws.zoom(px);
    }
    this.ws.setScrollTime(start);
  }
  probe(): WsProbe {
    const wrapper = this.ws.getWrapper();
    const root = wrapper.getRootNode() as ShadowRoot;
    const canvases = [...root.querySelectorAll("canvas"), ...this.container.querySelectorAll("canvas")];
    const doc = this.container.ownerDocument;
    let blank = 0;
    for (const c of canvases) {
      if (!c.width || !c.height) {
        blank++;
        continue;
      }
      const d = c.getContext("2d")!.getImageData(0, 0, c.width, c.height).data;
      let ink = false;
      for (let i = 3; i < d.length; i += 4 * 31) if (d[i]! > 0) {
        ink = true;
        break;
      }
      if (!ink) blank++;
    }
    return {
      canvases: canvases.length,
      blankCanvases: blank,
      canvasesInContainerDoc: canvases.filter((c) => c.ownerDocument === doc).length,
      wrapperOwnerIsContainerDoc: wrapper.ownerDocument === doc,
      regionStart: this.region?.start ?? -1,
      scrollPx: this.ws.getScroll(),
      wrapperWidth: wrapper.scrollWidth,
      errors: this.errors,
    };
  }
  destroy() {
    this.ws.destroy();
  }
}
