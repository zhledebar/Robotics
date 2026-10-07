const fs=require('fs'),vm=require('vm'),assert=require('assert');
const source=fs.readFileSync(__dirname+'/../source/owned_content_status.js','utf8');
const expected='https://www.dell.com/en-us/shop/laptop-computers/spd/xps16da16260/da16260_reg_01';
const actual=expected+'?variant=selected#configuration';
const env=vm.createContext({
 URL,
 location:{href:actual},
 document:{title:'Dell XPS 16',body:{innerText:'Dell XPS 16'},getElementById:()=>null},
 getComputedStyle:()=>({display:'block',visibility:'visible',contentVisibility:'visible',opacity:'1'})
});
const read=vm.runInContext('('+source+')',env);
const result=JSON.parse(read(expected));
assert.strictEqual(result.url,actual);
assert.strictEqual(result.regions['offers-container'].present,false);
assert.ok(!('price'in result)&&!('stock'in result),'diagnostic must not carry quote data');
assert.throws(()=>read('https://www.dell.com/en-us/shop/laptop-computers/spd/dellpro16laptoppc16250/pc16250_fixed_22'),/机型已变化/);
console.log(JSON.stringify({status:'PASS',sameModelQueryAndFragmentAccepted:true,wrongModelRejected:true,quoteFieldsAbsent:true}));
