fn private_fn() {
    println!("hello");
}

pub fn public_fn(x: i32) -> i32 {
    x + 1
}

pub(crate) fn crate_fn() {
    println!("crate");
}

struct PrivateStruct {
    field: i32,
}

pub struct PublicStruct {
    pub field: i32,
}

pub enum Direction {
    Up,
    Down,
}

trait PrivateTrait {
    fn method(&self);
}

pub trait PublicTrait {
    fn method(&self);
}

impl PublicStruct {
    fn method(&self) {}
}

const MAX: i32 = 100;
