// One WebGL2 renderer per window (R52; S5: Chrome's 16-context limit is shared by the opener and its popouts, so one
// context per view loses contexts). Every audio view of the window draws through it into its own 2D canvas
// (copy-out), so a view keeps its last image while the context is lost. Magnitudes live as R8 tiles in one
// TEXTURE_2D_ARRAY with LRU slots; a 256 × N RGB lookup texture holds the colormaps; gain, range, colormap and the
// frequency axis are uniforms, so changing them redraws without an upload. On restore the renderer rebuilds and the
// views re-upload from their sources' CPU caches.
import { COLORMAP_TABLES } from "./colormaps.gen";
import { FLOOR_DB, STEP_DB } from "./stft";
import { TILE } from "./tiles";

export const COLORMAPS = Object.keys(COLORMAP_TABLES) as Colormap[];
export type Colormap = keyof typeof COLORMAP_TABLES;
export const SLOT_BINS = 257;

export type SpecUniforms = { gainDb: number; rangeDb: number; peakDb: number; colormap: Colormap; axis: "mel" | "hz"; fmaxHz: number };
export type TileQuad = { key: string; bytes: Uint8Array; bins: number; binHz: number; srcFmaxHz: number; x0: number; x1: number; y0: number; y1: number };

const VS = `#version 300 es
in vec2 a_pos;
uniform vec4 u_rect;
out vec2 v_uv;
void main() {
  v_uv = a_pos;
  gl_Position = vec4(mix(u_rect.xy, u_rect.zw, a_pos), 0.0, 1.0);
}`;

const FS = `#version 300 es
precision highp float;
precision highp sampler2DArray;
in vec2 v_uv;
uniform sampler2DArray u_tiles;
uniform sampler2D u_lut;
uniform float u_layer, u_binHz, u_srcFmax, u_fmax, u_mel, u_cmap, u_cmaps;
uniform float u_gain, u_range, u_peak;
out vec4 o;
float mel(float f) { return 2595.0 * log(1.0 + f / 700.0) / log(10.0); }
float hz(float m) { return 700.0 * (pow(10.0, m / 2595.0) - 1.0); }
void main() {
  float f = u_mel > 0.5 ? hz(v_uv.y * mel(u_fmax)) : v_uv.y * u_fmax;
  if (f > u_srcFmax) { o = vec4(0.10, 0.10, 0.12, 1.0); return; }
  float bin = f / u_binHz;
  float q = texture(u_tiles, vec3(v_uv.x, (bin + 0.5) / ${SLOT_BINS}.0, u_layer)).r * 255.0;
  float db = q * ${STEP_DB.toFixed(2)} + (${FLOOR_DB.toFixed(1)});
  float t = clamp((db + u_gain - (u_peak - u_range)) / u_range, 0.0, 1.0);
  o = vec4(texture(u_lut, vec2((t * 255.0 + 0.5) / 256.0, (u_cmap + 0.5) / u_cmaps)).rgb, 1.0);
}`;

function b64(s: string): Uint8Array {
  return Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
}

type Prog = { p: WebGLProgram; u: Record<string, WebGLUniformLocation | null> };
const UNIFORMS = ["u_rect", "u_tiles", "u_lut", "u_layer", "u_binHz", "u_srcFmax", "u_fmax", "u_mel", "u_cmap", "u_cmaps", "u_gain", "u_range", "u_peak"];

export type RendererStats = { uploads: number; uploadBytes: number; draws: number; lost: number; restored: number; generation: number };

export class Renderer {
  readonly canvas: HTMLCanvasElement;
  readonly win: Window;
  readonly slotCount: number;
  private gl: WebGL2RenderingContext | null = null;
  private prog: Prog | null = null;
  private vao: WebGLVertexArrayObject | null = null;
  private tiles: WebGLTexture | null = null;
  private lut: WebGLTexture | null = null;
  private slots = new Map<string, number>(); // key → layer, in LRU order
  private free: number[] = []; // layers released by closed views, reused first
  private onRestore = new Set<() => void>();
  private vw = 1;
  private vh = 1;
  /** True when WebGL2 is unavailable: views draw nothing for the spectrogram and say so. */
  readonly unsupported: boolean;
  stats: RendererStats = { uploads: 0, uploadBytes: 0, draws: 0, lost: 0, restored: 0, generation: 0 };

  constructor(win: Window, slotCount = 64) {
    this.win = win;
    this.slotCount = slotCount;
    this.canvas = win.document.createElement("canvas");
    this.canvas.width = 1;
    this.canvas.height = 1;
    this.canvas.addEventListener("webglcontextlost", (e) => {
      e.preventDefault(); // allows the restore
      this.stats.lost++;
      this.gl = null;
    });
    this.canvas.addEventListener("webglcontextrestored", () => {
      this.stats.restored++;
      this.init();
      for (const f of this.onRestore) f();
    });
    this.unsupported = !this.init();
  }

  get lost(): boolean {
    return this.gl === null || this.gl.isContextLost();
  }

  whenRestored(fn: () => void): () => void {
    this.onRestore.add(fn);
    return () => void this.onRestore.delete(fn);
  }

  private init(): boolean {
    const gl = this.canvas.getContext("webgl2", { antialias: false, alpha: false, preserveDrawingBuffer: false, premultipliedAlpha: false });
    if (!gl) return false;
    this.stats.generation++;
    this.gl = gl;
    this.slots.clear();
    this.free = [];
    const p = gl.createProgram();
    for (const [type, src] of [
      [gl.VERTEX_SHADER, VS],
      [gl.FRAGMENT_SHADER, FS],
    ] as const) {
      const s = gl.createShader(type);
      if (!s) return false;
      gl.shaderSource(s, src);
      gl.compileShader(s);
      if (!gl.getShaderParameter(s, gl.COMPILE_STATUS) && !gl.isContextLost()) throw new Error(gl.getShaderInfoLog(s) ?? "shader");
      gl.attachShader(p, s);
    }
    gl.bindAttribLocation(p, 0, "a_pos");
    gl.linkProgram(p);
    const u: Prog["u"] = {};
    for (const n of UNIFORMS) u[n] = gl.getUniformLocation(p, n);
    this.prog = { p, u };
    this.vao = gl.createVertexArray();
    gl.bindVertexArray(this.vao);
    gl.bindBuffer(gl.ARRAY_BUFFER, gl.createBuffer());
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([0, 0, 1, 0, 0, 1, 1, 1]), gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 0, 0);
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
    this.tiles = gl.createTexture();
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D_ARRAY, this.tiles);
    gl.texStorage3D(gl.TEXTURE_2D_ARRAY, 1, gl.R8, TILE, SLOT_BINS, this.slotCount);
    for (const [k, v] of [
      [gl.TEXTURE_MIN_FILTER, gl.LINEAR],
      [gl.TEXTURE_MAG_FILTER, gl.LINEAR],
      [gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE],
      [gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE],
    ] as const)
      gl.texParameteri(gl.TEXTURE_2D_ARRAY, k, v);
    this.lut = gl.createTexture();
    gl.activeTexture(gl.TEXTURE1);
    gl.bindTexture(gl.TEXTURE_2D, this.lut);
    const rows = COLORMAPS.length;
    const lut = new Uint8Array(256 * 3 * rows);
    COLORMAPS.forEach((name, i) => lut.set(b64(COLORMAP_TABLES[name]), i * 768));
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGB8, 256, rows, 0, gl.RGB, gl.UNSIGNED_BYTE, lut);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
    return true;
  }

  /** Forces a context loss (WEBGL_lose_context) and restores after ms: tests and the S5 measurements. */
  loseContext(ms = 100): boolean {
    const ext = this.gl?.getExtension("WEBGL_lose_context");
    if (!ext) return false;
    ext.loseContext();
    this.win.setTimeout(() => ext.restoreContext(), ms);
    return true;
  }

  private slot(key: string, bytes: Uint8Array, bins: number): number {
    const gl = this.gl!;
    const s = this.slots.get(key);
    if (s !== undefined) {
      this.slots.delete(key);
      this.slots.set(key, s);
      return s;
    }
    let layer: number;
    if (this.slots.size < this.slotCount) layer = this.slots.size;
    else {
      const [oldest, l] = this.slots.entries().next().value!;
      this.slots.delete(oldest);
      layer = l;
    }
    gl.activeTexture(gl.TEXTURE0);
    gl.texSubImage3D(gl.TEXTURE_2D_ARRAY, 0, 0, 0, layer, TILE, bins, 1, gl.RED, gl.UNSIGNED_BYTE, bytes);
    this.stats.uploads++;
    this.stats.uploadBytes += bytes.byteLength;
    this.slots.set(key, layer);
    return layer;
  }

  /** Releases the slots of one source (a view closed or its data changed): they become the first to reuse. */
  release(prefix: string): void {
    const keep = [...this.slots].filter(([k]) => !k.startsWith(prefix));
    const freed = [...this.slots].filter(([k]) => k.startsWith(prefix)).map(([, l]) => l);
    if (freed.length === 0) return;
    // Rebuild the order so freed layers are reused before any live tile is evicted.
    this.slots = new Map(keep);
    this.free.push(...freed);
  }

  /**
   * Draws a view's spectrogram: `paint` issues quads in pixel coordinates (top-left origin); the result is copied
   * into target, the view's own 2D canvas in whichever document the view lives. False when the context is lost.
   */
  drawInto(target: HTMLCanvasElement, quads: TileQuad[], u: SpecUniforms): boolean {
    const gl = this.gl;
    if (!gl || gl.isContextLost() || !this.prog) return false;
    const w = target.width;
    const h = target.height;
    if (w === 0 || h === 0) return true;
    if (this.canvas.width < w || this.canvas.height < h) {
      this.canvas.width = Math.max(this.canvas.width, w);
      this.canvas.height = Math.max(this.canvas.height, h);
    }
    const H = this.canvas.height;
    gl.viewport(0, H - h, w, h);
    gl.enable(gl.SCISSOR_TEST);
    gl.scissor(0, H - h, w, h);
    gl.clearColor(0.06, 0.06, 0.08, 1);
    gl.clear(gl.COLOR_BUFFER_BIT);
    this.vw = w;
    this.vh = h;
    const p = this.prog;
    gl.useProgram(p.p);
    gl.bindVertexArray(this.vao);
    gl.uniform1i(p.u.u_tiles!, 0);
    gl.uniform1i(p.u.u_lut!, 1);
    gl.uniform1f(p.u.u_fmax!, u.fmaxHz);
    gl.uniform1f(p.u.u_mel!, u.axis === "mel" ? 1 : 0);
    gl.uniform1f(p.u.u_cmap!, Math.max(0, COLORMAPS.indexOf(u.colormap)));
    gl.uniform1f(p.u.u_cmaps!, COLORMAPS.length);
    gl.uniform1f(p.u.u_gain!, u.gainDb);
    gl.uniform1f(p.u.u_range!, u.rangeDb);
    gl.uniform1f(p.u.u_peak!, u.peakDb);
    gl.activeTexture(gl.TEXTURE1);
    gl.bindTexture(gl.TEXTURE_2D, this.lut);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D_ARRAY, this.tiles);
    for (const q of quads) {
      const layer = this.slotFor(q);
      gl.uniform1f(p.u.u_layer!, layer);
      gl.uniform1f(p.u.u_binHz!, q.binHz);
      gl.uniform1f(p.u.u_srcFmax!, q.srcFmaxHz);
      this.rect(p.u.u_rect!, q.x0, q.y0, q.x1, q.y1);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    }
    gl.disable(gl.SCISSOR_TEST);
    const ctx = target.getContext("2d");
    if (!ctx) return true;
    ctx.drawImage(this.canvas, 0, 0, w, h, 0, 0, w, h);
    this.stats.draws++;
    return true;
  }

  private slotFor(q: TileQuad): number {
    if (!this.slots.has(q.key) && this.free.length > 0) {
      const layer = this.free.pop()!;
      const gl = this.gl!;
      gl.activeTexture(gl.TEXTURE0);
      gl.texSubImage3D(gl.TEXTURE_2D_ARRAY, 0, 0, 0, layer, TILE, q.bins, 1, gl.RED, gl.UNSIGNED_BYTE, q.bytes);
      this.stats.uploads++;
      this.stats.uploadBytes += q.bytes.byteLength;
      this.slots.set(q.key, layer);
      return layer;
    }
    return this.slot(q.key, q.bytes, q.bins);
  }

  private rect(u: WebGLUniformLocation, x0: number, y0: number, x1: number, y1: number): void {
    this.gl!.uniform4f(u, (x0 / this.vw) * 2 - 1, 1 - (y1 / this.vh) * 2, (x1 / this.vw) * 2 - 1, 1 - (y0 / this.vh) * 2);
  }

  /** Waits for the GPU, so a measured draw includes its raster time. */
  finish(): void {
    this.gl?.finish();
  }
}

const perWindow = new WeakMap<Window, Renderer>();

/** The window's renderer, created on first use with slotCount tile slots (views.audio.tile_slots). */
export function sharedRenderer(win: Window, slotCount = 64): Renderer {
  let r = perWindow.get(win);
  if (!r) {
    r = new Renderer(win, slotCount);
    perWindow.set(win, r);
  }
  return r;
}

/** The renderer of a window if one exists (tests, measurements). */
export function rendererOf(win: Window): Renderer | undefined {
  return perWindow.get(win);
}
