function localFunc() {
    return 1;
}

export function exportedFunc(a, b) {
    return a + b;
}

export default function defaultFunc() {
    return 0;
}

export async function asyncExported() {
    return 0;
}

class LocalClass {
    method() {}
}

export class ExportedClass {
    method() {}
}

const localConst = 42;

export const exportedConst = 99;
