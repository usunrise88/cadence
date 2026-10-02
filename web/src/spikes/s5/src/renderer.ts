// One WebGL2 renderer per window (R52): every audio view in the window draws through it into its own 2D canvas.
// Magnitudes live as R8 tiles in one TEXTURE_2D_ARRAY (LRU slots); a 256x3 RGB lookup texture holds the colormaps;
// gain, range, colormap and the frequency axis are uniforms, so changing them is a redraw without an upload.
// Context loss: the views keep their last image (2D canvases), the renderer rebuilds on restore and re-uploads from
// the sources' CPU caches.
import { GREY, MAGMA, VIRIDIS } from "./colormaps.gen";
import { TILE, FLOOR_DB, STEP_DB } from "./sources";

export const COLORMAPS = ["magma", "viridis", "grey"] as const;
export type Colormap = (typeof COLORMAPS)[number];
const SLOT_BINS = 257;

export type SpecUniforms = { gainDb: number; rangeDb: number; peakDb: number; colormap: Colormap; axis: "mel" | "hz"; fmaxHz: number };
export type TileQuad = { key: string; bytes: Uint8Array; bins: number; binHz: number; srcFmaxHz: number; x0: number; x1: number; y0: number; y1: number };
export type FeatQuad = { key: string; data: Uint16Array; width: number; mels: number; x0: number; x1: number; y0: number; y1: number; lo: number; hi: number; dimAbove: number; colormap: Colormap };

const VS = `#version 300 es
in vec2 a_pos; // unit quad
uniform vec4 u_rect; // x0, y0, x1, y1 in clip space
out vec2 v_uv;
void main() {
  v_uv = a_pos;
  gl_Position = vec4(mix(u_rect.xy, u_rect.zw, a_pos), 0.0, 1.0);
}`;

const FS_SPEC = `#version 300 es
precision highp float;
precision highp sampler2DArray;
in vec2 v_uv;
uniform sampler2DArray u_tiles;
uniform sampler2D u_lut;
uniform float u_layer, u_bins, u_binHz, u_srcFmax, u_fmax, u_mel, u_cmap;
uniform float u_gain, u_range, u_peak;
out vec4 o;
float mel(float f) { return 2595.0 * log(1.0 + f / 700.0) / log(10.0); }
float hz(float m) { return 700.0 * (pow(10.0, m / 2595.0) - 1.0); }
void main() {
  float f = u_mel > 0.5 ? hz(v_uv.y * mel(u_fmax)) : v_uv.y * u_fmax;
  if (f > u_srcFmax) { o = vec4(0.10, 0.10, 0.12, 1.0); return; }   // above the origin's Nyquist
  float bin = f / u_binHz;
  float q = texture(u_tiles, vec3(v_uv.x, (bin + 0.5) / ${SLOT_BINS}.0, u_layer)).r * 255.0;
  float db = q * ${STEP_DB.toFixed(2)} + (${FLOOR_DB.toFixed(1)});
  float t = clamp((db + u_gain - (u_peak - u_range)) / u_range, 0.0, 1.0);
  o = vec4(texture(u_lut, vec2((t * 255.0 + 0.5) / 256.0, (u_cmap + 0.5) / 3.0)).rgb, 1.0);
}`;

const FS_FEAT = `#version 300 es
precision highp float;
in vec2 v_uv;
uniform sampler2D u_feat;
uniform sampler2D u_lut;
uniform float u_lo, u_hi, u_cmap, u_dim, u_mels;
out vec4 o;
void main() {
  float v = texture(u_feat, v_uv).r;
  float t = clamp((v - u_lo) / (u_hi - u_lo), 0.0, 1.0);
  vec3 c = texture(u_lut, vec2((t * 255.0 + 0.5) / 256.0, (u_cmap + 0.5) / 3.0)).rgb;
  if (u_dim > 0.0 && v_uv.y * u_mels >= u_dim) c *= 0.35;   // filters above 4 kHz on 8 kHz audio: dither only
  o = vec4(c, 1.0);
}`;

function b64(s: string): Uint8Array {
  return Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
}

type Prog = { p: WebGLProgram; u: Record<string, WebGLUniformLocation | null> };

export type RendererStats = { contexts: number; uploads: number; uploadBytes: number; draws: number; lost: number; restored: number; gpuBytes: number; generation: number };
export const globalStats = { contextsCreated: 0, contextsLostUnforced: 0 };

export class Renderer {
  readonly canvas: HTMLCanvasElement;
  gl: WebGL2RenderingContext | null = null;
  private spec?: Prog;
  private feat?: Prog;
  private vao?: WebGLVertexArrayObject;
  private tiles?: WebGLTexture;
  private lut?: WebGLTexture;
  private slots = new Map<string, number>(); // key → layer (LRU order)
  private featTex = new Map<string, WebGLTexture>();
  private onRestore = new Set<() => void>();
  forcedLoss = false;
  stats: RendererStats = { contexts: 0, uploads: 0, uploadBytes: 0, draws: 0, lost: 0, restored: 0, gpuBytes: 0, generation: 0 };

  constructor(
    readonly win: Window,
    readonly slotCount = 128,
  ) {
    this.canvas = win.document.createElement("canvas");
    this.canvas.width = 1;
    this.canvas.height = 1;
    this.canvas.addEventListener("webglcontextlost", (e) => {
      e.preventDefault();
      this.stats.lost++;
      if (!this.forcedLoss) globalStats.contextsLostUnforced++;
      this.gl = null;
    });
    this.canvas.addEventListener("webglcontextrestored", () => {
      this.stats.restored++;
      this.forcedLoss = false;
      this.init();
      for (const f of this.onRestore) f();
    });
    this.init();
    allRenderers.add(this);
  }

  get lost() {
    return this.gl === null || this.gl.isContextLost();
  }
  whenRestored(fn: () => void) {
    this.onRestore.add(fn);
    return () => void this.onRestore.delete(fn);
  }

  private init() {
    const gl = this.canvas.getContext("webgl2", { antialias: false, alpha: false, preserveDrawingBuffer: false, premultipliedAlpha: false });
    if (!gl) throw new Error("WebGL2 unavailable");
    if (this.stats.generation === 0) {
      globalStats.contextsCreated++;
      this.stats.contexts = 1;
    }
    this.stats.generation++;
    this.gl = gl;
    this.slots.clear();
    this.featTex.clear();
    const prog = (fs: string, names: string[]): Prog => {
      const p = gl.createProgram()!;
      for (const [type, src] of [
        [gl.VERTEX_SHADER, VS],
        [gl.FRAGMENT_SHADER, fs],
      ] as const) {
        const s = gl.createShader(type)!;
        gl.shaderSource(s, src);
        gl.compileShader(s);
        if (!gl.getShaderParameter(s, gl.COMPILE_STATUS) && !gl.isContextLost()) throw new Error(gl.getShaderInfoLog(s) ?? "shader");
        gl.attachShader(p, s);
      }
      gl.bindAttribLocation(p, 0, "a_pos");
      gl.linkProgram(p);
      const u: Prog["u"] = {};
      for (const n of names) u[n] = gl.getUniformLocation(p, n);
      return { p, u };
    };
    this.spec = prog(FS_SPEC, ["u_rect", "u_tiles", "u_lut", "u_layer", "u_bins", "u_binHz", "u_srcFmax", "u_fmax", "u_mel", "u_cmap", "u_gain", "u_range", "u_peak"]);
    this.feat = prog(FS_FEAT, ["u_rect", "u_feat", "u_lut", "u_lo", "u_hi", "u_cmap", "u_dim", "u_mels"]);
    this.vao = gl.createVertexArray()!;
    gl.bindVertexArray(this.vao);
    const buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([0, 0, 1, 0, 0, 1, 1, 1]), gl.STATIC_DRAW);
    gl.enableVertexAttribArray(0);
    gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 0, 0);
    gl.pixelStorei(gl.UNPACK_ALIGNMENT, 1);
    this.tiles = gl.createTexture()!;
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
    this.lut = gl.createTexture()!;
    gl.activeTexture(gl.TEXTURE1);
    gl.bindTexture(gl.TEXTURE_2D, this.lut);
    const lut = new Uint8Array(256 * 3 * 3);
    lut.set(b64(MAGMA), 0);
    lut.set(b64(VIRIDIS), 768);
    lut.set(b64(GREY), 1536);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGB8, 256, 3, 0, gl.RGB, gl.UNSIGNED_BYTE, lut);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
    this.stats.gpuBytes = TILE * SLOT_BINS * this.slotCount + 256 * 3 * 3;
  }

  /** Forces a context loss (WEBGL_lose_context) and restores after `ms`. */
  loseContext(ms = 100) {
    const ext = this.gl?.getExtension("WEBGL_lose_context");
    if (!ext) return;
    this.forcedLoss = true;
    ext.loseContext();
    this.win.setTimeout(() => ext.restoreContext(), ms);
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

  /**
   * Draws one view's image: `paint` issues quads in pixel coordinates (origin top-left of the view), then the result
   * is copied into `target` (the view's own 2D canvas, in whichever document the view lives).
   */
  drawInto(target: HTMLCanvasElement, paint: (r: Renderer, w: number, h: number) => void): boolean {
    const gl = this.gl;
    if (!gl || gl.isContextLost()) return false;
    const w = target.width;
    const h = target.height;
    if (w === 0 || h === 0) return true;
    if (this.canvas.width < w || this.canvas.height < h) {
      this.canvas.width = Math.max(this.canvas.width, w);
      this.canvas.height = Math.max(this.canvas.height, h);
    }
    const H = this.canvas.height;
    gl.viewport(0, H - h, w, h);
    gl.disable(gl.SCISSOR_TEST);
    gl.clearColor(0.06, 0.06, 0.08, 1);
    gl.enable(gl.SCISSOR_TEST);
    gl.scissor(0, H - h, w, h);
    gl.clear(gl.COLOR_BUFFER_BIT);
    this.vw = w;
    this.vh = h;
    paint(this, w, h);
    gl.disable(gl.SCISSOR_TEST);
    const ctx = target.getContext("2d")!;
    ctx.drawImage(this.canvas, 0, 0, w, h, 0, 0, w, h);
    this.stats.draws++;
    return true;
  }
  private vw = 1;
  private vh = 1;
  private rect(u: WebGLUniformLocation | null, x0: number, y0: number, x1: number, y1: number) {
    // pixel coords (top-left origin) → clip space
    this.gl!.uniform4f(u, (x0 / this.vw) * 2 - 1, 1 - (y1 / this.vh) * 2, (x1 / this.vw) * 2 - 1, 1 - (y0 / this.vh) * 2);
  }

  tileQuads(quads: TileQuad[], u: SpecUniforms) {
    const gl = this.gl!;
    const p = this.spec!;
    gl.useProgram(p.p);
    gl.bindVertexArray(this.vao!);
    gl.uniform1i(p.u.u_tiles!, 0);
    gl.uniform1i(p.u.u_lut!, 1);
    gl.uniform1f(p.u.u_fmax!, u.fmaxHz);
    gl.uniform1f(p.u.u_mel!, u.axis === "mel" ? 1 : 0);
    gl.uniform1f(p.u.u_cmap!, COLORMAPS.indexOf(u.colormap));
    gl.uniform1f(p.u.u_gain!, u.gainDb);
    gl.uniform1f(p.u.u_range!, u.rangeDb);
    gl.uniform1f(p.u.u_peak!, u.peakDb);
    gl.activeTexture(gl.TEXTURE1);
    gl.bindTexture(gl.TEXTURE_2D, this.lut!);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D_ARRAY, this.tiles!);
    for (const q of quads) {
      const layer = this.slot(q.key, q.bytes, q.bins);
      gl.uniform1f(p.u.u_layer!, layer);
      gl.uniform1f(p.u.u_bins!, q.bins);
      gl.uniform1f(p.u.u_binHz!, q.binHz);
      gl.uniform1f(p.u.u_srcFmax!, q.srcFmaxHz);
      this.rect(p.u.u_rect!, q.x0, q.y0, q.x1, q.y1);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    }
  }

  featQuads(quads: FeatQuad[]) {
    const gl = this.gl!;
    const p = this.feat!;
    gl.useProgram(p.p);
    gl.bindVertexArray(this.vao!);
    gl.uniform1i(p.u.u_feat!, 2);
    gl.uniform1i(p.u.u_lut!, 1);
    gl.activeTexture(gl.TEXTURE1);
    gl.bindTexture(gl.TEXTURE_2D, this.lut!);
    for (const q of quads) {
      let tex = this.featTex.get(q.key);
      gl.activeTexture(gl.TEXTURE2);
      if (!tex) {
        tex = gl.createTexture()!;
        gl.bindTexture(gl.TEXTURE_2D, tex);
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.R16F, q.width, q.mels, 0, gl.RED, gl.HALF_FLOAT, q.data);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
        this.stats.uploads++;
        this.stats.uploadBytes += q.data.byteLength;
        this.featTex.set(q.key, tex);
        if (this.featTex.size > 8) {
          const [k, t] = this.featTex.entries().next().value!;
          gl.deleteTexture(t);
          this.featTex.delete(k);
        }
      } else gl.bindTexture(gl.TEXTURE_2D, tex);
      gl.uniform1f(p.u.u_lo!, q.lo);
      gl.uniform1f(p.u.u_hi!, q.hi);
      gl.uniform1f(p.u.u_cmap!, COLORMAPS.indexOf(q.colormap));
      gl.uniform1f(p.u.u_dim!, q.dimAbove);
      gl.uniform1f(p.u.u_mels!, q.mels);
      this.rect(p.u.u_rect!, q.x0, q.y0, q.x1, q.y1);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    }
  }

  /** Waits for the GPU (SwiftShader in headless runs) so a measured draw includes its raster time. */
  finish() {
    this.gl?.finish();
  }
}

const perWindow = new WeakMap<Window, Renderer>();
export function sharedRenderer(win: Window): Renderer {
  let r = perWindow.get(win);
  if (!r) {
    r = new Renderer(win);
    perWindow.set(win, r);
  }
  return r;
}
export const allRenderers = new Set<Renderer>();
