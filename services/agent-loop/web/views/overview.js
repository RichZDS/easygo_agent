import { state } from '../ui.js';
import { wallet } from './wallet.js';
import { tasks } from './workshop.js';

export async function load() {
  const generation = state.generation;
  await wallet();
  if (generation === state.generation) await tasks(true);
}
export function bind() {}
