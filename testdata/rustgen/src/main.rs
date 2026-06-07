// Emits authoritative serde_columnar 0.3.14 golden vectors for the loro-go port.
// Inner #[columnar(vec)] rows live inside a parent #[columnar] table with a
// class="vec" field (the way loro actually invokes the column strategies).
// Per-line: strategy|type|csv|fullhex|field_count|col_count|column_hex
use serde_columnar::columnar;

macro_rules! row_and_table {
    ($row:ident, $table:ident, $t:ty, $strat:literal) => {
        #[columnar(vec, ser, de)]
        #[derive(Clone)]
        struct $row {
            #[columnar(strategy = $strat)]
            v: $t,
        }
        #[columnar(ser, de)]
        struct $table {
            #[columnar(class = "vec")]
            r: Vec<$row>,
        }
    };
}

row_and_table!(RleU64, TRleU64, u64, "Rle");
row_and_table!(RleU8, TRleU8, u8, "Rle");
row_and_table!(RleI32, TRleI32, i32, "Rle");
row_and_table!(RleU32, TRleU32, u32, "Rle");
row_and_table!(DeltaRleU32, TDeltaRleU32, u32, "DeltaRle");
row_and_table!(DeltaRleI32, TDeltaRleI32, i32, "DeltaRle");
row_and_table!(DodI64, TDodI64, i64, "DeltaOfDelta");
row_and_table!(BoolRleRow, TBoolRle, bool, "BoolRle");

fn read_varint(b: &[u8], i: &mut usize) -> u64 {
    let mut x = 0u64;
    let mut s = 0u32;
    loop {
        let c = b[*i];
        *i += 1;
        if c < 0x80 {
            x |= (c as u64) << s;
            break;
        }
        x |= ((c & 0x7f) as u64) << s;
        s += 7;
    }
    x
}

fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{:02x}", x)).collect()
}

fn line(strat: &str, ty: &str, csv: &str, full: &[u8]) {
    let mut i = 0usize;
    let fc = read_varint(full, &mut i);
    let cc = read_varint(full, &mut i);
    let len = read_varint(full, &mut i) as usize;
    let col = &full[i..i + len];
    println!("{}|{}|{}|{}|{}|{}|{}", strat, ty, csv, hex(full), fc, cc, hex(col));
}

macro_rules! emit {
    ($strat:expr, $ty:expr, $csv:expr, $table:ident, $rows:expr) => {{
        let full = serde_columnar::to_vec(&$table { r: $rows }).unwrap();
        line($strat, $ty, $csv, &full);
    }};
}

fn main() {
    emit!("Rle", "u64", "1000,1000,2,2,2", TRleU64, vec![RleU64{v:1000},RleU64{v:1000},RleU64{v:2},RleU64{v:2},RleU64{v:2}]);
    emit!("Rle", "u8", "5,5,5", TRleU8, vec![RleU8{v:5},RleU8{v:5},RleU8{v:5}]);
    emit!("Rle", "i32", "-1,-1,-1", TRleI32, vec![RleI32{v:-1},RleI32{v:-1},RleI32{v:-1}]);
    emit!("Rle", "u32", "7,8,9", TRleU32, vec![RleU32{v:7},RleU32{v:8},RleU32{v:9}]);
    emit!("Rle", "u64", "", TRleU64, Vec::<RleU64>::new());
    emit!("BoolRle", "bool", "true,true,false,false,false", TBoolRle, vec![BoolRleRow{v:true},BoolRleRow{v:true},BoolRleRow{v:false},BoolRleRow{v:false},BoolRleRow{v:false}]);
    emit!("BoolRle", "bool", "false,true", TBoolRle, vec![BoolRleRow{v:false},BoolRleRow{v:true}]);
    emit!("BoolRle", "bool", "", TBoolRle, Vec::<BoolRleRow>::new());
    emit!("DeltaRle", "u32", "1,2,3,4,5,6", TDeltaRleU32, vec![DeltaRleU32{v:1},DeltaRleU32{v:2},DeltaRleU32{v:3},DeltaRleU32{v:4},DeltaRleU32{v:5},DeltaRleU32{v:6}]);
    emit!("DeltaRle", "i32", "0,5,10,10,10", TDeltaRleI32, vec![DeltaRleI32{v:0},DeltaRleI32{v:5},DeltaRleI32{v:10},DeltaRleI32{v:10},DeltaRleI32{v:10}]);
    emit!("DeltaOfDelta", "i64", "1,2,3,4,5,6", TDodI64, vec![DodI64{v:1},DodI64{v:2},DodI64{v:3},DodI64{v:4},DodI64{v:5},DodI64{v:6}]);
    emit!("DeltaOfDelta", "i64", "", TDodI64, Vec::<DodI64>::new());
    emit!("DeltaOfDelta", "i64", "0", TDodI64, vec![DodI64{v:0}]);
    emit!("DeltaOfDelta", "i64", "5", TDodI64, vec![DodI64{v:5}]);
    emit!("DeltaOfDelta", "i64", "100,200,400,700,1100", TDodI64, vec![DodI64{v:100},DodI64{v:200},DodI64{v:400},DodI64{v:700},DodI64{v:1100}]);
    emit!("DeltaOfDelta", "i64", "10,5,30,1,1000", TDodI64, vec![DodI64{v:10},DodI64{v:5},DodI64{v:30},DodI64{v:1},DodI64{v:1000}]);
}
