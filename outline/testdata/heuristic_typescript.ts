function localFunc(): void {
    return;
}

export function exportedFunc(a: number, b: number): number {
    return a + b;
}

export default function defaultFunc(): void {
    return;
}

export async function asyncExported(): Promise<void> {
    return;
}

class LocalClass {
    method(): void {}
}

export class ExportedClass {
    method(): void {}
}

export interface ExportedInterface {
    name: string;
}

interface LocalInterface {
    value: number;
}

export const exportedConst: number = 99;

export enum Direction {
    Up,
    Down,
}
