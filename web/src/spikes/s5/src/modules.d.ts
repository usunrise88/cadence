declare module "@pffft/pffft.js" {
  const PFFFT: (options: object) => Promise<unknown>;
  export default PFFFT;
}
declare module "fourier-transform" {
  export function fft(input: Float32Array | Float64Array, output?: [Float64Array, Float64Array]): [Float64Array, Float64Array];
}
